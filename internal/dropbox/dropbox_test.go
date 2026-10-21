package dropbox

import (
	"strings"
	"testing"
)

// An explicit scope parameter restricts the token to exactly what is listed,
// so every scope any API call needs must appear here. account_info.read was
// once missing, which broke users/get_current_account with missing_scope on
// real accounts even though Dropbox grants that scope to every app.
func TestScopesCoverEveryAPICallWeMake(t *testing.T) {
	required := []string{
		"account_info.read",   // users/get_current_account
		"files.metadata.read", // files/list_folder
		"files.content.read",  // files/download
		"files.content.write", // files/upload
	}
	for _, want := range required {
		found := false
		for _, s := range Scopes {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Scopes is missing %q; the token will 401 with missing_scope", want)
		}
	}
}

func TestAuthorizeURLCarriesTheScopes(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	url := AuthorizeURL("test-key", p)
	if !strings.Contains(url, "account_info.read") {
		t.Errorf("authorize URL does not request account_info.read: %s", url)
	}
	if !strings.Contains(url, "token_access_type=offline") {
		t.Error("authorize URL must request a refresh token")
	}
}
