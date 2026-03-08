#!/bin/bash

# Обновление системы
apt-get update
apt-get upgrade -y

# Установка curl
apt-get install -y curl

# Добавление репозитория NodeSource для последней версии (23.x)
curl -fsSL https://deb.nodesource.com/setup_23.x | bash -

# Установка Node.js
apt-get install -y nodejs

# Проверка версии
node --version
npm --version

echo "Node.js установлен!"