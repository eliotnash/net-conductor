# Third-party components

- Go standard library and golang.org/x/{crypto,net,sys}: BSD-style license. Source and versions are pinned in go.mod/go.sum. License copies are in licenses/.
- github.com/gorilla/websocket: BSD-2-Clause. License copy in licenses/.
- xterm.js and addon-fit: MIT. License copies in licenses/; distributed JS/CSS remain under these licenses.
- Electron: MIT, with Chromium and other notices supplied in the desktop distribution's LICENSE and LICENSES.chromium.html. Electron version is pinned in desktop/package-lock.json.
- The current desktop and favicon assets retain Electron's default application icon. They are not original Net Conductor branding. Electron's license is also included in licenses/electron-MIT.txt.
- WireGuard for Windows is installed separately from the official signed MSI when missing; its own license and source terms apply. https://git.zx2c4.com/wireguard-windows/ and https://www.wireguard.com/install/
- OpenSSH is a separate operating-system component. https://www.openssh.com/
- Pion WebRTC and its transport modules: MIT; github.com/pkg/sftp: BSD-2-Clause. Additional transitive dependency license copies are included in licenses/. Versions are pinned in go.mod/go.sum. WebRTC discovery currently uses Cloudflare's public STUN service.

WireGuard, Electron, Chromium and their names are owned by their respective projects. This project is not an official distribution of those projects.
