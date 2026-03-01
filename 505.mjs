import { fork } from 'child_process'; // вместо Worker
import { readFile } from 'fs/promises';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import TelegramBot from 'node-telegram-bot-api';
import WebSocket from 'ws';
import { exec } from 'child_process';

const itemsJson = await readFile('items.json');
let items = JSON.parse(itemsJson);
const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

const token = '8181447110:AAGOIwXMfXm_0-a0JCtNsPOMNoSY7eQ4ZKI';

const tgBot = new TelegramBot(token, { polling: true });

const infoChatID = -4709535234
const alertChatID = -4763690917
const pomoikaChatID = -4896488855

const bots = [
  { username: 'bugulmark2', password: 'ggggg', anarchy: 5005, type: '4narek', inventoryPort: 3002, balance: undefined, msgID: 0, msgTime: null, isManualStop: false, itemPrices: items, item: 'netherite leggings' },
  { username: 'otstalyibolvan', password: 'ggggg', anarchy: 5005, type: '4narek', inventoryPort: 3000, balance: undefined, msgID: 0, msgTime: null, isManualStop: false, itemPrices: items, item: 'netherite leggings' },
  { username: 'zbnennabite', password: 'ggggg', anarchy: 5005, type: '4narek', inventoryPort: 3002, balance: undefined, msgID: 0, msgTime: null, isManualStop: false, itemPrices: items, item: 'netherite leggings' },
   { username: 'sashapshonkoumer', password: 'ggggg', anarchy: 5006, type: '4narek', inventoryPort: 3002, balance: undefined, msgID: 0, msgTime: null, isManualStop: false, itemPrices: items, item: 'netherite chestplate' },
  { username: 'ahahaetopravda', password: 'ggggg', anarchy: 5006, type: '4narek', inventoryPort: 3000, balance: undefined, msgID: 0, msgTime: null, isManualStop: false, itemPrices: items, item: 'netherite chestplate' },
  { username: 'ochenlubludashu', password: 'ggggg', anarchy: 5006, type: '4narek', inventoryPort: 3002, balance: undefined, msgID: 0, msgTime: null, isManualStop: false, itemPrices: items, item: 'netherite chestplate' },
];


// Храним дочерние процессы вместо воркеров
let childProcesses = [];
let botItems = new Map();
let botInventory = new Map();

let socket;
let isSocketOpen = false;

function runBotProcess(bot) {
  return new Promise((resolve, reject) => {
    const scriptPath = join(__dirname, `${bot.type}.mjs`);
    
    // Передаём данные бота через аргументы командной строки (или через env)
    const env = {
      ...process.env,
      BOT_USERNAME: bot.username,
      BOT_PASSWORD: bot.password,
      BOT_ANARCHY: bot.anarchy,
      BOT_ITEM: bot.item,
      BOT_PORT: bot.inventoryPort,
    };

    const child = fork(scriptPath, [], {
      env,
      silent: false,
    });

    bot.isManualStop = false;
    bot.lastRestartTime = Date.now();
    childProcesses.push({ child, bot });

    // Таймаут на успешный запуск
    setTimeout(() => {
      if (!bot.success) {
        console.warn(`⏱ ${bot.username} не ответил успехом за 30 секунд. Убиваем.`);
        child.kill();
      }
    }, 30000);

    // Ограничение времени жизни (1 час)
    setTimeout(() => {
      console.log(`⏲️ Процесс ${bot.username} отработал 1 час. Завершаем.`);
      child.kill();
    }, 1800000);

    child.on('message', async (message) => {
      if (message.name === 'success') {
        const botToUpdate = bots.find(b => b.username === message.username);
        if (botToUpdate) {
          botToUpdate.success = true;
          console.log(`✅ ${message.username} успешно запущен`);
        }
      } else if (message.name === 'buy') {
        socket?.send(JSON.stringify({ action: 'buy', type: message.id }));
      } else if (message.name === 'sell') {
        socket?.send(JSON.stringify({ action: 'sell', type: message.id }));
      } else if (message.name === 'items') {
        botItems.set(message.username, message.items);
      } else if (message.name === 'try-sell') {
        socket?.send(JSON.stringify({ action: 'try-sell', type: message.id }));
      } else if (message.name === 'inventory') {
        botInventory.set(message.username, message.data);
      } else if (message.name === 'buying') {
        socket?.send(JSON.stringify({ action: 'add', json_data: message.data }));
      } else {
        tgBot.sendMessage(alertChatID, message);
      }
    });

    const handleRestart = () => {
      // Удаляем процесс из списка
      childProcesses = childProcesses.filter(p => p.child !== child);

      if (!bot.isManualStop) {
        setTimeout(() => {
          console.log(`🔁 Перезапуск бота ${bot.username} через 20 секунд`);
          runBotProcess(bot);
        }, 20000);
      }
    };

    child.on('error', (error) => {
      bot.success = false;
      console.error(`❌ Process error (${bot.username}): ${error}`);
      tgBot.sendMessage(alertChatID, `${bot.username} вырубился с ошибкой`);
      handleRestart();
    });

    child.on('exit', (code) => {
      bot.success = false;
      console.warn(`⚠️ Process ${bot.username} завершился с кодом ${code}`);
      tgBot.sendMessage(alertChatID, `${bot.username} вырубился`);
      handleRestart();
    });
  });
}

function stopProcesses() {
  bots.forEach(bot => {
    bot.isManualStop = true;
  });
  childProcesses.forEach(p => p.child.kill());
  childProcesses = [];
}

function gitPull() {
  return new Promise((resolve, reject) => {
    exec('git pull', (err, stdout, stderr) => {
      if (err) reject(`Error executing git pull: ${stderr}`);
      else resolve(stdout);
    });
  });
}

async function startBots() {
  bots.forEach(bot => bot.itemPrices = items);
  const botPromises = bots.map(bot => runBotProcess(bot));
  try {
    setTimeout(() => socket?.send(JSON.stringify({ action: 'info' })), 1000);
    await Promise.all(botPromises);
    console.log('All bots finished');
  } catch (error) {
    console.error('Error in bot execution:', error);
  }
}

async function restartBots() {
  bots.forEach(bot => bot.itemPrices = items);
  const botPromises = bots.map(bot => runBotProcess(bot));
  try {
    setTimeout(() => socket?.send(JSON.stringify({ action: 'info' })), 3000);
    await Promise.all(botPromises);
    console.log('All bots finished');
  } catch (error) {
    console.error('Error in bot execution:', error);
  }
}

// Telegram команды
tgBot.onText(/\/update/, async (msg) => {
  if ((Date.now() / 1000) - msg.date > 10) return;
  try {
    stopProcesses();
    const pullResult = await gitPull();
    tgBot.sendMessage(alertChatID, `Git pull выполнен:\n${pullResult}`);
    await restartBots();
  } catch (error) {
    tgBot.sendMessage(alertChatID, `Произошла ошибка: ${error.message}`);
  }
});

tgBot.onText(/\/start/, async (msg) => {
  if ((Date.now() / 1000) - msg.date > 10) return;
  tgBot.sendMessage(alertChatID, 'Перезапуск ботов');
  await restartBots();
});

tgBot.onText(/\/stop/, async (msg) => {
  if ((Date.now() / 1000) - msg.date > 10) return;
  tgBot.sendMessage(alertChatID, 'Остановка ботов');
  stopProcesses();
});

function connectWebSocket() {
  socket = new WebSocket('ws://85.198.86.42:8080/ws');

  socket.on('open', () => {
    console.log('✅ Подключено к серверу WebSocket');
    isSocketOpen = true;
    socket.send(JSON.stringify({ action: 'info' }));
  });

  socket.on('message', (data) => {
    try {
      const dataObj = JSON.parse(data);

      if (dataObj.action === 'json_update' && Array.isArray(dataObj.data)) {
        childProcesses.forEach(p => p.child.send({ 
          type: 'items_buying', 
          data: dataObj.data 
        }));
      } else if (dataObj.prices) {
        items = items.map(item => ({
          ...item,
          priceSell: dataObj.prices[item.id],
          ratio: dataObj.ratios[item.id]
        }));
        bots.forEach(bot => bot.itemPrices = items);

        console.log('📦 Обновлены цены:', items.map(i => `${i.id}: ${i.priceSell}`));

        childProcesses.forEach(p => p.child.send({ type: 'price', data: items }));

        if (!botsStarted && items.every(i => i.priceSell)) {
          botsStarted = true;
          startBots();
        }
      }
    } catch (e) {
      console.error('Ошибка обработки сообщения от сервера:', e.message);
    }
  });

  socket.on('close', () => {
    console.log('❌ WebSocket отключён. Реконнект через 5 секунд...');
    isSocketOpen = false;
    setTimeout(connectWebSocket, 5000);
  });

  socket.on('error', (err) => {
    console.error('⚠️ Ошибка WebSocket:', err.message);
  });
}

setInterval(() => {
  if (isSocketOpen) {
    const itemsCount = new Map();
    const itemsCountInventory = new Map();

    for (let items of Array.from(botItems.values())) {
      for (let item of items) {
        const count = itemsCount.get(item);
        if (count) itemsCount.set(item, count + 1);
        else itemsCount.set(item, 1);
      }
    }

    for (let items of Array.from(botInventory.values())) {
      for (let item of items) {
        const count = itemsCountInventory.get(item);
        if (count) itemsCountInventory.set(item, count + 1);
        else itemsCountInventory.set(item, 1);
      }
    }

    const ah = Object.fromEntries(itemsCount);
    const inv = Object.fromEntries(itemsCountInventory);
    socket.send(JSON.stringify({ action: 'presence', items: ah, inventory: inv }));
  }
}, 30000);

let botsStarted = false;
connectWebSocket();