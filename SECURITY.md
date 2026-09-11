# Security reports

Do not include private keys, device tokens, admin passwords, proxy credentials or private terminal content in public issues. Report vulnerabilities privately to the repository owner.

This preview has a single trusted administrator. The control server can establish SSH sessions using its configured key. Deploy it only within the intended trust boundary. Browser SSH validates a pinned host-key fingerprint and does not offer a bypass.

Remote sessions require authenticated server signaling. WebRTC binds data-channel peers to the SDP exchanged over that signaling channel. If direct connectivity fails, operation messages use authenticated WebSocket relay connections; the trusted server can read relayed data. Device tokens are not sent to browser clients. SSH and SFTP on Windows authenticate to localhost using a dedicated key authorized for the installation user, not an unrestricted SYSTEM shell. The key file is restricted to SYSTEM/Administrators. Remote desktop input runs in the logged-in desktop user's process; it does not bypass Windows secure desktop or privilege boundaries. Do not grant administrator access to untrusted users.

The Windows service handles privileged network operations through a fixed API: register a device, diagnose, restart its configured WireGuard service, and share a loopback proxy port with the server. Remote terminal commands run through the authenticated SSH user's session; there is no general-purpose SYSTEM shell API.
