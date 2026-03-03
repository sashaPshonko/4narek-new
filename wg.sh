#!/bin/bash
set -e

# Проверяем curl
if ! command -v curl &> /dev/null; then
    echo "Ошибка: curl не установлен. Установите: sudo apt install curl"
    exit 1
fi

# Публичный IP сервера
SERVER_IP=$(curl -s ifconfig.me)
if [[ -z "$SERVER_IP" || "$SERVER_IP" == *"error"* ]]; then
    read -p "Введите публичный IP сервера вручную: " SERVER_IP
    while [[ -z "$SERVER_IP" ]]; do
        read -p "Повторите ввод: " SERVER_IP
    done
fi
echo "Публичный IP сервера: $SERVER_IP"

# Основной сетевой интерфейс
DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}')
echo "Основной интерфейс: $DEFAULT_IFACE"

# Установка WireGuard
sudo apt update
sudo apt install -y wireguard

# Генерация ключей сервера
SERVER_PRIVKEY=$(wg genkey)
SERVER_PUBKEY=$(echo "$SERVER_PRIVKEY" | wg pubkey)
sudo mkdir -p /etc/wireguard
echo "$SERVER_PRIVKEY" | sudo tee /etc/wireguard/server_private.key >/dev/null
echo "$SERVER_PUBKEY" | sudo tee /etc/wireguard/server_public.key >/dev/null
sudo chmod 600 /etc/wireguard/*.key

# Генерация ключей клиента
CLIENT_PRIVKEY=$(wg genkey)
CLIENT_PUBKEY=$(echo "$CLIENT_PRIVKEY" | wg pubkey)
CLIENT_IP="10.0.0.2"

# Конфиг сервера
sudo tee /etc/wireguard/wg0.conf >/dev/null <<EOL
[Interface]
PrivateKey = $SERVER_PRIVKEY
Address = 10.0.0.1/24
ListenPort = 51820
PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -t nat -A POSTROUTING -o $DEFAULT_IFACE -j MASQUERADE
PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -t nat -D POSTROUTING -o $DEFAULT_IFACE -j MASQUERADE

[Peer]
PublicKey = $CLIENT_PUBKEY
AllowedIPs = $CLIENT_IP/32
EOL

# Конфиг клиента
tee wg-client.conf >/dev/null <<EOL
[Interface]
PrivateKey = $CLIENT_PRIVKEY
Address = $CLIENT_IP/32
DNS = 8.8.8.8, 1.1.1.1

[Peer]
PublicKey = $SERVER_PUBKEY
Endpoint = $SERVER_IP:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
EOL

# Включение IP forwarding
echo "net.ipv4.ip_forward=1" | sudo tee -a /etc/sysctl.conf
sudo sysctl -p

# Запуск WireGuard
sudo wg-quick up wg0
sudo systemctl enable wg-quick@wg0

echo "=============================================="
echo "✅ Настройка завершена!"
echo "📄 Клиентский конфиг: $(pwd)/wg-client.conf"
echo "🌐 IP сервера в VPN: 10.0.0.1"
echo "💻 IP клиента в VPN: 10.0.0.2"
echo "=============================================="