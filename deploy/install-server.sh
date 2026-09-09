#!/usr/bin/env bash
set -euo pipefail
if [[ $EUID -ne 0 ]]; then echo 'Run as root.' >&2; exit 1; fi
: "${NC_PUBLIC_IP:?Set NC_PUBLIC_IP to the public IPv4 address}"
base=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
apt-get update
apt-get install -y wireguard iptables openssh-client
install -d -m 700 /var/lib/net-conductor /var/lib/net-conductor/backups
install -d -m 755 /opt/net-conductor
if [[ -f /etc/wireguard/wg0.conf ]]; then
 cp -a /etc/wireguard/wg0.conf "/var/lib/net-conductor/backups/wg0-$(date +%Y%m%d-%H%M%S).conf"
else
 install -d -m 700 /etc/wireguard
 umask 077
 private=$(wg genkey)
 cat > /etc/wireguard/wg0.conf <<EOF
[Interface]
Address = 10.77.0.1/24
ListenPort = 51820
PrivateKey = $private
PostUp = iptables -A FORWARD -i %i -o %i -j ACCEPT
PostDown = iptables -D FORWARD -i %i -o %i -j ACCEPT
EOF
 unset private
fi
printf 'net.ipv4.ip_forward=1\n' > /etc/sysctl.d/90-net-conductor.conf
sysctl -p /etc/sysctl.d/90-net-conductor.conf
systemctl enable --now wg-quick@wg0
install -m 755 "$base/dist/netconductor-linux-amd64" /opt/net-conductor/netconductor
if [[ ! -f /var/lib/net-conductor/config.json ]]; then
 /opt/net-conductor/netconductor -mode init-server -config /var/lib/net-conductor/config.json
fi
if [[ ! -f /var/lib/net-conductor/ssh_ed25519 ]]; then
 ssh-keygen -t ed25519 -N '' -f /var/lib/net-conductor/ssh_ed25519 -C net-conductor
fi
install -m 644 "$base/deploy/net-conductor.service" /etc/systemd/system/net-conductor.service
systemctl daemon-reload
systemctl enable --now net-conductor
echo 'Ready. Allow TCP 18443 and UDP 51820 in the cloud security group.'
echo 'Admin password: /var/lib/net-conductor/config.json (adminToken field).'
echo 'Import server.crt in clients; authorize ssh_ed25519.pub on target devices for browser SSH.'
