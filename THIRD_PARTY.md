# Third-party components

The MIT license at the repository root applies to this project's original
source. It does not replace licenses or notices belonging to other projects.

- [go-qrcode](https://github.com/skip2/go-qrcode): QR-code dependency. Its
  license and copyright notices remain in the Go module distribution.
- [minui-list](https://github.com/josegonzalez/minui-list),
  [minui-presenter](https://github.com/josegonzalez/minui-presenter), and
  [minui-keyboard](https://github.com/josegonzalez/minui-keyboard): external
  handheld UI helpers, downloaded during packaging. Their source and license
  notices are available in the respective repositories. Forks distributing
  those binaries must comply with their licenses.
- The embedded CA certificate bundle in `internal/dropbox/certs/cacert.pem`
  includes its own source and license information.

Delta, NextUI, Dropbox, and the emulator cores are separate projects. They
are credited for interoperability; this repository does not relicense them.
