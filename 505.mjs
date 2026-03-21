import { Worker } from 'worker_threads';
import { readFile, writeFile } from 'fs/promises';
import { existsSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import TelegramBot from 'node-telegram-bot-api';
import WebSocket from 'ws';
import { exec } from 'child_process';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// Проверяем существование файла items.json
const itemsPath = join(__dirname, 'items.json');
const botsPath = join(__dirname, '505.json');
let items = [];
let bots = [];

const token = '8321775652:AAFs6ZYxyb820LkCG_oq2fy6F9D4_XzVW3U';
const tgBot = new TelegramBot(token, { polling: true });
const alertChatID = -1003827870631;

let workers = [];
let botItems = new Map();
let botInventory = new Map();
let itemsBuying = [];
let socket;
let isSocketOpen = false;
let botsStarted = false;
setInterval(async () => {
    try {
        await cleanNPM();
    } catch (error) {
        await sendErrorToTelegram(error, 'auto_clean_log');
    }
}, 5 * 60 * 60 * 1000);

// ========== УНИВЕРСАЛЬНАЯ ФУНКЦИЯ ОТПРАВКИ ОШИБОК ==========
async function sendErrorToTelegram(error, context = '', extraData = null) {
    const timestamp = new Date().toLocaleString('ru-RU');
    let errorMessage = error?.message || String(error);
    let errorStack = error?.stack ? `\n📚 Stack: ${error.stack.substring(0, 500)}` : '';
    
    const fullMessage = `❌ [${timestamp}] ${context}\n📝 ${errorMessage}${errorStack}`;
    
    console.error(fullMessage);
    
    if (extraData) {
        console.error('📦 Дополнительные данные:', extraData);
    }
    
    try {
        const truncatedMessage = fullMessage.substring(0, 4000);
        await tgBot.sendMessage(alertChatID, truncatedMessage);
    } catch (tgError) {
        console.error('❌ Не удалось отправить ошибку в Telegram:', tgError.message);
    }
}

// Обертка для безопасного вызова асинхронных функций
async function safeExecute(fn, context, ...args) {
    try {
        return await fn(...args);
    } catch (error) {
        await sendErrorToTelegram(error, context, { args: JSON.stringify(args).substring(0, 500) });
        throw error;
    }
}

// Функция загрузки конфигурации ботов
async function loadBotsConfig() {
    try {
        if (existsSync(botsPath)) {
            const botsJson = await readFile(botsPath, 'utf-8');
            const loadedBots = JSON.parse(botsJson);
            
            bots = loadedBots.map(bot => ({
                ...bot,
                itemPrices: items,
                msgID: 0,
                msgTime: null,
                isManualStop: false,
                success: false
            }));
            
            console.log(`✅ bots.json успешно загружен (${bots.length} ботов)`);
        } else {
            console.warn('⚠️ bots.json не найден');
            bots = [];
        }
    } catch (error) {
        await sendErrorToTelegram(error, 'loadBotsConfig', { botsPath });
        bots = [];
    }
}

// Функция загрузки предметов
async function loadItemsConfig() {
    try {
        if (existsSync(itemsPath)) {
            const itemsJson = await readFile(itemsPath, 'utf-8');
            items = JSON.parse(itemsJson);
            console.log(`✅ items.json успешно загружен (${items.length} предметов)`);
        } else {
            console.warn('⚠️ items.json не найден');
            items = [];
        }
    } catch (error) {
        await sendErrorToTelegram(error, 'loadItemsConfig', { itemsPath });
        items = [];
    }
}

// Инициализация конфигураций
try {
    await loadItemsConfig();
    await loadBotsConfig();
} catch (error) {
    await sendErrorToTelegram(error, 'initialization');
}

function runWorker(bot) {
    try {
        // Если уже есть активный воркер для этого бота — не запускаем повторно
        workers
            .filter(w => w.workerData?.username === bot.username)
            .forEach(w => {
                try { w.terminate(); } catch (e) { 
                    sendErrorToTelegram(e, 'terminateWorker', { username: bot.username });
                }
            });
        workers = workers.filter(w => w.workerData?.username !== bot.username);
    } catch (error) {
        sendErrorToTelegram(error, 'runWorker_cleanup', { username: bot.username });
    }

    return new Promise((resolve, reject) => {
        try {
            const workerScriptPath = join(__dirname, `${bot.type}.mjs`);
            const worker = new Worker(workerScriptPath, {
                workerData: bot,
                resourceLimits: {
                    maxOldGenerationSizeMb: 200,
                }
            });

            bot.isManualStop = false;
            bot.lastRestartTime = Date.now();
            workers.push(worker);

            // Убить если неуспешный запуск за 30 сек
            const timeoutId = setTimeout(() => {
                if (!bot.success) {
                    console.warn(`⏱ ${bot.username} не ответил успехом за 30 секунд. Убиваем.`);
                    try {
                        worker.terminate();
                    } catch (e) {
                        sendErrorToTelegram(e, 'timeout_terminate', { username: bot.username });
                    }
                }
            }, 30000);

            worker.on('message', async (message) => {
                try {
                    if (message.name === 'success') {
                        const botToUpdate = bots.find(b => b.username === message.username);
                        if (botToUpdate) {
                            botToUpdate.success = true;
                            console.log(`✅ ${message.username} успешно запущен`);
                        }
                    } else if (message.name === "buy") {
                        try {
                            socket?.send(JSON.stringify({ action: 'buy', type: message.id, price: message.price }));
                        } catch (socketError) {
                            await sendErrorToTelegram(socketError, 'send_buy_message', { message });
                        }
                    } else if (message.name === "sell") {
                        try {
                            socket?.send(JSON.stringify({ action: 'sell', type: message.id, price: message.price }));
                        } catch (socketError) {
                            await sendErrorToTelegram(socketError, 'send_sell_message', { message });
                        }
                    } else if (message.name === "items") {
                        try {
                            botItems.set(message.username, message.items);
                        } catch (itemsError) {
                            await sendErrorToTelegram(itemsError, 'set_items', { username: message.username });
                        }
                    } else if (message.name === "try-sell") {
                        try {
                            socket?.send(JSON.stringify({ action: "try-sell", type: message.id }));
                        } catch (socketError) {
                            await sendErrorToTelegram(socketError, 'send_trysell_message', { message });
                        }
                    } else if (message.name === "inventory") {
                        try {
                            botInventory.set(message.username, message.data);
                        } catch (invError) {
                            await sendErrorToTelegram(invError, 'set_inventory', { username: message.username });
                        }
                    } else if (message.name === "buying") {
                        try {
                            await broadcastBuyingLocally(message.data);
                            socket?.send(JSON.stringify({ action: "add", json_data: message.data }));
                        } catch (buyingError) {
                            await sendErrorToTelegram(buyingError, 'process_buying', { message });
                        }
                    } else if (typeof message === 'string' && message.includes('ввести капчу')) {
                        try {
                            await tgBot.sendMessage(alertChatID, `⚠️ ${message}`);
                            console.log(`📨 Капча: ${message}`);
                        } catch (tgError) {
                            await sendErrorToTelegram(tgError, 'send_captcha', { message });
                        }
                    } else if (message.name === "set_min_price") {
                        try {
                            socket?.send(JSON.stringify({ 
                                action: 'set_min_price', 
                                type: message.type, 
                                price: message.price 
                            }));
                            console.log(`📉 Установка минимальной цены для ${message.type}: ${message.price}`);
                        } catch (socketError) {
                            await sendErrorToTelegram(socketError, 'send_min_price', { message });
                        }
                    } else if (message.name === "set_max_price") {
                        try {
                            socket?.send(JSON.stringify({ 
                                action: 'set_max_price', 
                                type: message.type, 
                                price: message.price 
                            }));
                            console.log(`📈 Установка максимальной цены для ${message.type}: ${message.price}`);
                        } catch (socketError) {
                            await sendErrorToTelegram(socketError, 'send_max_price', { message });
                        }
                    } else {
                        if (typeof message === 'string') {
                            try {
                                await tgBot.sendMessage(alertChatID, `📝 ${message}`);
                            } catch (tgError) {
                                await sendErrorToTelegram(tgError, 'send_string_message', { message });
                            }
                        }
                    }
                } catch (error) {
                    await sendErrorToTelegram(error, 'worker_message_handler', { 
                        username: bot.username, 
                        messageType: message?.name || typeof message 
                    });
                    try {
                        await tgBot.sendMessage(alertChatID, `❌ Ошибка в main: ${error.message}`);
                    } catch (e) {
                        console.error('Не удалось отправить ошибку в ТГ:', e.message);
                    }
                }
            });

            const handleRestart = () => {
                try {
                    workers = workers.filter(w => w !== worker);

                    if (!bot.isManualStop) {
                        setTimeout(() => {
                            console.log(`🔁 Перезапуск бота ${bot.username} через 60 секунд`);
                            runWorker(bot);
                        }, 60000);
                    }
                } catch (restartError) {
                    sendErrorToTelegram(restartError, 'handleRestart', { username: bot.username });
                }
            };

            worker.on('error', async (error) => {
                bot.success = false;
                await sendErrorToTelegram(error, 'worker_error', { username: bot.username });
                try {
                    await tgBot.sendMessage(alertChatID, `${bot.username} вырубился с ошибкой ${error.message}`);
                } catch (tgError) {
                    console.error('Не удалось отправить в ТГ:', tgError.message);
                }
            });

            worker.on('exit', async (code) => {
                bot.success = false;
                console.warn(`⚠️ Worker ${bot.username} завершился с кодом ${code}`);
                // if (code !== 0) {
                //     await sendErrorToTelegram(`Exit code: ${code}`, 'worker_exit', { username: bot.username });
                // }
                handleRestart();
            });

            resolve(worker);
        } catch (error) {
            sendErrorToTelegram(error, 'runWorker_create', { username: bot.username });
            reject(error);
        }
    });
}

// Функция для локальной синхронизации покупаемых предметов
async function broadcastBuyingLocally(uuid) {
    return new Promise((resolve, reject) => {
        try {
            const updatedBuying = [...itemsBuying, uuid];
            
            workers.forEach(w => {
                try {
                    w.postMessage({ 
                        type: 'items_buying', 
                        data: updatedBuying 
                    });
                } catch (postError) {
                    sendErrorToTelegram(postError, 'broadcast_postMessage', { uuid });
                }
            });
            
            itemsBuying = updatedBuying;
            resolve();
        } catch (localError) {
            sendErrorToTelegram(localError, 'broadcastBuyingLocally', { uuid });
            reject(localError);
        }
    });
}

function stopWorkers() {
    try {
        bots.forEach(bot => {
            bot.isManualStop = true;
        });
    } catch (error) {
        sendErrorToTelegram(error, 'stopWorkers_setFlags');
    }
    
    return new Promise((resolve, reject) => {
        try {
            workers.forEach(worker => {
                try {
                    worker.terminate();
                } catch (termError) {
                    sendErrorToTelegram(termError, 'terminate_single_worker');
                }
            });
            workers = [];
            resolve('All workers stopped');
        } catch (error) {
            sendErrorToTelegram(error, 'stopWorkers');
            reject('Error stopping workers: ' + error);
        }
    });
}

function cleanNPM() {
    return new Promise((resolve, reject) => {
    const logPath = './bot.log';
    
    exec(`> ${logPath}`, (err, stdout, stderr) => {
      if (err) {
        reject(`Error cleaning bot.log: ${stderr || err.message}`);
      } else {
        console.log(`✅ bot.log очищен (${logPath})`);
        resolve(stdout);
      }
    });
  });
}

function gitPull() {
    return new Promise((resolve, reject) => {
        exec('git pull', (err, stdout, stderr) => {
            if (err) {
                sendErrorToTelegram(err, 'gitPull', { stderr });
                reject(`Error executing git pull: ${stderr}`);
            } else {
                resolve(stdout);
            }
        });
    });
}

async function startBots() {
    try {
        // Перезагружаем конфигурацию ботов перед стартом
        await loadBotsConfig();
        
        // Обновляем цены у ботов
        bots.forEach(bot => bot.itemPrices = items);
        
        const botPromises = bots.map(bot => runWorker(bot));
        try {
            setTimeout(() => {
                try {
                    socket?.send(JSON.stringify({ action: "info" }));
                } catch (socketError) {
                    sendErrorToTelegram(socketError, 'startBots_socketInfo');
                }
            }, 1000);
            const results = await Promise.all(botPromises);
            console.log('All bots started:');
        } catch (error) {
            await sendErrorToTelegram(error, 'startBots_execution');
            console.error('Error in bot execution:', error);
        }
    } catch (error) {
        await sendErrorToTelegram(error, 'startBots');
    }
}

async function restartBots() {
    try {
        console.log('🔄 Перезапуск всех ботов...');
        
        // Останавливаем текущих ботов
        await stopWorkers();
        
        // Перезагружаем конфигурации
        await loadItemsConfig();
        await loadBotsConfig();
        
        // Обновляем цены у ботов
        bots.forEach(bot => bot.itemPrices = items);
        
        // Запускаем новых ботов
        const botPromises = bots.map(bot => runWorker(bot));
        try {
            setTimeout(() => {
                try {
                    socket?.send(JSON.stringify({ action: "info" }));
                } catch (socketError) {
                    sendErrorToTelegram(socketError, 'restartBots_socketInfo');
                }
            }, 3000);
            const results = await Promise.all(botPromises);
            console.log('All bots restarted:');
        } catch (error) {
            await sendErrorToTelegram(error, 'restartBots_execution');
            console.error('Error in bot execution:', error);
        }
    } catch (error) {
        await sendErrorToTelegram(error, 'restartBots');
    }
}

// Telegram команды
tgBot.onText(/\/update/, async (msg) => {
    try {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, '🔄 Обновление через git pull...');
        await stopWorkers();
        const pullResult = await gitPull();
        await tgBot.sendMessage(alertChatID, `✅ Git pull выполнен:\n${pullResult}`);
    } catch (error) {
        await sendErrorToTelegram(error, 'update_command');
        await tgBot.sendMessage(alertChatID, `❌ Ошибка при обновлении: ${error.message}`);
    } finally {
        process.exit(1);
    }
});

tgBot.onText(/\/ping/, async (msg) => {
    try {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, `✅ работает`);
    } catch (error) {
        await sendErrorToTelegram(error, 'ping_command');
    }
});

tgBot.onText(/\/start/, async (msg) => {
    try {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, '🔄 Перезапуск ботов');
        await restartBots();
    } catch (error) {
        await sendErrorToTelegram(error, 'start_command');
    }
});

tgBot.onText(/\/stop/, async (msg) => {
    try {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, '⏹ Остановка ботов');
        await stopWorkers();
    } catch (error) {
        await sendErrorToTelegram(error, 'stop_command');
    }
});

tgBot.onText(/\/reload/, async (msg) => {
    try {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, '🔄 Перезагрузка конфигурации...');
        await stopWorkers();
        await restartBots();
        await tgBot.sendMessage(alertChatID, '✅ Конфигурация перезагружена');
    } catch (error) {
        await sendErrorToTelegram(error, 'reload_command');
        try {
            await tgBot.sendMessage(alertChatID, `❌ Ошибка: ${error.message}`);
        } catch (tgError) {
            console.error('Не удалось отправить в ТГ:', tgError.message);
        }
    }
});

function connectWebSocket() {
    try {
        socket = new WebSocket('ws://85.198.86.42:8080/ws');

        socket.on('open', () => {
            console.log('✅ Подключено к серверу WebSocket');
            isSocketOpen = true;
            try {
                socket.send(JSON.stringify({ action: "info" }));
            } catch (sendError) {
                sendErrorToTelegram(sendError, 'websocket_open_send');
            }
        });

        socket.on('message', async (data) => {
            try {
                const dataObj = JSON.parse(data);
                
                if (dataObj.action === "json_update" && Array.isArray(dataObj.data)) {
                    workers.forEach(w => {
                        try {
                            w.postMessage({ 
                                type: 'items_buying', 
                                data: dataObj.data 
                            });
                        } catch (postError) {
                            sendErrorToTelegram(postError, 'json_update_postMessage');
                        }
                    });
                } else if (dataObj.prices) {
                    let freshItems = [];
                    try {
                        if (existsSync(itemsPath)) {
                            const itemsJson = await readFile(itemsPath, 'utf-8');
                            freshItems = JSON.parse(itemsJson);
                            console.log('✅ items.json перечитан заново');
                        } else {
                            console.warn('⚠️ items.json не найден');
                            freshItems = [];
                        }
                    } catch (error) {
                        await sendErrorToTelegram(error, 'read_items_on_update', { dataObj: Object.keys(dataObj) });
                        freshItems = [];
                    }
                    
                    try {
                        const itemsWithPrice = freshItems
                            .map(item => ({
                                ...item,
                                priceSell: dataObj.prices[item.id],
                                ratio: dataObj.ratios?.[item.id] || item.ratio || 0.8
                            }))
                            .filter(item => item.priceSell !== undefined && item.priceSell !== null);
                        
                        const itemsWithoutPrice = freshItems.filter(item => 
                            dataObj.prices[item.id] === undefined || dataObj.prices[item.id] === null
                        );
                        
                        if (itemsWithoutPrice.length > 0) {
                            console.log('⚠️ Предметы без цен (удалены):', itemsWithoutPrice.map(i => i.id).join(', '));
                        }
                        
                        items = itemsWithPrice;
                        bots.forEach(bot => bot.itemPrices = items);

                        console.log('📦 Обновлены цены для', items.length, 'предметов');
                        workers.forEach(w => {
                            try {
                                w.postMessage({ type: 'price', data: items });
                            } catch (postError) {
                                sendErrorToTelegram(postError, 'price_update_postMessage');
                            }
                        });

                        if (!botsStarted && items.length > 0) {
                            botsStarted = true;
                            startBots();
                        }
                    } catch (processError) {
                        await sendErrorToTelegram(processError, 'process_prices_update');
                    }
                }
            } catch (e) {
                await sendErrorToTelegram(e, 'websocket_message_handler', { dataLength: data?.length });
            }
        });

        socket.on('close', () => {
            console.log('❌ WebSocket отключён. Реконнект через 5 секунд...');
            isSocketOpen = false;
            setTimeout(connectWebSocket, 5000);
        });

        socket.on('error', async (err) => {
            await sendErrorToTelegram(err, 'WebSocket_error');
        });
    } catch (error) {
        sendErrorToTelegram(error, 'connectWebSocket');
        setTimeout(connectWebSocket, 5000);
    }
}

// setInterval с обработкой ошибок
setInterval(() => {
    try {
        if (isSocketOpen) {
            const itemsCount = new Map();
            const itemsCountInventory = new Map();
            
            try {
                for (let itemsList of Array.from(botItems.values())) {
                    for (let item of itemsList) {
                        const count = itemsCount.get(item);
                        if (count) {
                            itemsCount.set(item, count + 1);
                        } else {
                            itemsCount.set(item, 1);
                        }
                    }
                }
                
                for (let itemsList of Array.from(botInventory.values())) {
                    for (let item of itemsList) {
                        const count = itemsCountInventory.get(item);
                        if (count) {
                            itemsCountInventory.set(item, count + 1);
                        } else {
                            itemsCountInventory.set(item, 1);
                        }
                    }
                }
                
                const ah = Object.fromEntries(itemsCount);
                const inv = Object.fromEntries(itemsCountInventory);
                socket.send(JSON.stringify({ action: "presence", items: ah, inventory: inv }));
            } catch (processError) {
                sendErrorToTelegram(processError, 'presence_interval_process');
            }
        }
    } catch (error) {
        sendErrorToTelegram(error, 'presence_interval');
    }
}, 30000);

// ========== ГЛОБАЛЬНЫЕ ОБРАБОТЧИКИ НЕОТЛОВЛЕННЫХ ОШИБОК ==========
process.on('unhandledRejection', async (reason, promise) => {
    await sendErrorToTelegram(reason, 'unhandledRejection', { 
        promise: promise?.toString?.()?.substring(0, 200) || 'unknown' 
    });
});

process.on('uncaughtException', async (error) => {
    await sendErrorToTelegram(error, 'uncaughtException');
    // Даем время отправить сообщение
    setTimeout(() => {
        process.exit(1);
    }, 3000);
});


console.log('🔍 ДИАГНОСТИКА:');
console.log('  - workers:', workers.length);
console.log('  - bots:', bots.length);
console.log('  - socket:', socket ? 'created' : 'not created');
console.log('  - isSocketOpen:', isSocketOpen);

// Принудительно держим процесс
const keepAliveInterval = setInterval(() => {
    console.log(`🏃‍♂️ [${new Date().toISOString()}] Процесс жив. Workers: ${workers.length}`);
}, 10000);

// Ловим выход
process.on('beforeExit', (code) => {
    console.log(`⚠️ beforeExit с кодом ${code}`);
    console.log('  - workers:', workers.length);
    console.log('  - socket readyState:', socket?.readyState);
    clearInterval(keepAliveInterval);
});

process.on('exit', (code) => {
    console.log(`⚠️ exit с кодом ${code}`);
});

// Запуск WebSocket
connectWebSocket();