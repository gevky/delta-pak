package main

import (
	"context"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gevky/delta-pak/internal/config"
)

// The handheld's own keyboard is the worst possible place to enter a ~43
// character Dropbox code, and shuttling the SD card to a computer to paste it
// into a file is worse. Both exist only because the code has to travel from
// the phone to the handheld.
//
// The handheld is on Wi-Fi, so it can just ask for it: this serves a small
// page on the local network, and the phone -- which already has the code on
// screen and a real keyboard with copy and paste -- posts it back. Nothing is
// typed on the device and the card never moves.
//
// It deliberately does not use an OAuth redirect back to the device: Dropbox
// only redirects to URIs registered in the app console, and a handheld's LAN
// address changes. Pasting the code sidesteps that entirely.

const loginPage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect Dropbox</title>
<style>
 body{font:17px/1.5 -apple-system,system-ui,sans-serif;margin:0;padding:24px;
      background:#111;color:#eee;max-width:34rem}
 h1{font-size:1.3rem;margin:0 0 1rem}
 ol{padding-left:1.2rem} li{margin:.9rem 0}
 a.btn,button{display:block;width:100%;box-sizing:border-box;text-align:center;
      background:#0061fe;color:#fff;text-decoration:none;padding:15px;
      border:0;border-radius:10px;font-size:17px;font-weight:600;margin:.6rem 0}
 input{width:100%;box-sizing:border-box;padding:14px;font-size:17px;
      border-radius:10px;border:1px solid #444;background:#1c1c1c;color:#eee}
 .ok{color:#4ade80} .err{color:#f87171}
</style>
<h1>Connect Dropbox to {{.Device}}</h1>
{{if .Message}}<p class="{{.Class}}">{{.Message}}</p>{{end}}
{{if not .Done}}
<ol>
 <li>Tap the button. Approve access in Dropbox.</li>
 <li>Dropbox shows you a code. <b>Copy</b> it.</li>
 <li>Paste it below and press Connect.</li>
</ol>
<a class="btn" href="{{.AuthURL}}" target="_blank" rel="noopener">Open Dropbox</a>
<form method="POST" action="/submit">
 <input name="code" placeholder="Paste the code here"
        autocapitalize="off" autocorrect="off" spellcheck="false" autofocus>
 <button type="submit">Connect</button>
</form>
{{end}}
`

type loginPageData struct {
	Device  string
	AuthURL string
	Message string
	Class   string
	Done    bool
}

// lanIP returns the address other devices on the network can reach.
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		return ipnet.IP.String()
	}
	return ""
}

// serveLogin runs the pairing page until the account is linked or ctx expires.
// The QR path, when set, receives a code for the page's own URL.
func serveLogin(ctx context.Context, cfg *config.Config, home, qrPath, screen string, port int) error {
	ip := lanIP()
	if ip == "" {
		return fmt.Errorf("this device is not on a network, so the phone cannot reach it.\nConnect Wi-Fi and try again, or use the SD card method")
	}

	// Generate (or reuse) the pending authorisation the page will point at.
	authURL, err := authorizeURLFor(cfg, home)
	if err != nil {
		return err
	}

	pageURL := fmt.Sprintf("http://%s:%d/", ip, port)
	if qrPath != "" {
		w, h := parseScreen(screen)
		if err := writeAuthQR(pageURL, qrPath, w, h); err != nil {
			fmt.Fprintf(os.Stderr, "could not write QR code: %v\n", err)
		}
	}

	tmpl := template.Must(template.New("p").Parse(loginPage))
	done := make(chan error, 1)
	mux := http.NewServeMux()

	render := func(w http.ResponseWriter, d loginPageData) {
		d.Device = "your handheld"
		d.AuthURL = authURL
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = tmpl.Execute(w, d)
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		render(w, loginPageData{})
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSpace(r.FormValue("code"))
		if code == "" {
			render(w, loginPageData{Message: "Please paste the code.", Class: "err"})
			return
		}
		if err := linkFinish(r.Context(), cfg, home, code); err != nil {
			// Keep serving: the user can correct a truncated paste in place.
			render(w, loginPageData{
				Message: "Dropbox rejected that code: " + firstLine(err.Error()),
				Class:   "err",
			})
			return
		}
		render(w, loginPageData{
			Message: "Connected as " + cfg.AccountName + ". You can close this page.",
			Class:   "ok", Done: true,
		})
		select {
		case done <- nil:
		default:
		}
	})

	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			select {
			case done <- err:
			default:
			}
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Println(pageURL)

	select {
	case err := <-done:
		if err != nil {
			return err
		}
		// Give the phone a moment to receive the success page.
		time.Sleep(500 * time.Millisecond)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for the phone")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
