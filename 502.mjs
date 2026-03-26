import { Worker } from 'worker_threads';
import { readFile } from 'fs/promises';
import { existsSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import TelegramBot from 'node-telegram-bot-api';
import WebSocket from 'ws';
import { exec } from 'child_process';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// ========== КОНСТАНТЫ ==========
const itemsPath = join(__dirname, 'items.json');
const botsPath = join(__dirname, '502.json');
const token = '8053905786:AAEN59tDF_yvC8G5Qw7TEgVOdhZ122bby1I';
const alertChatID = -1003827870631;
const WEBSOCKET_URL = 'ws://85.198.86.42:8080/ws';

// ========== ГЛОБАЛЬНЫЕ СОСТОЯНИЯ ==========
let items = [];
let bots = new Map(); // Map<username, botConfig>
let workers = new Map(); // Map<username, { worker, timeoutId }>
let botItems = new Map();
let botInventory = new Map();
let itemsBuying = [];
let socket;
let isSocketOpen = false;
let botsStarted = false;
let tgBot;
let isShuttingDown = false;

// ========== ФУНКЦИЯ ОТПРАВКИ АЛЕРТОВ ==========
async function sendAlert(message) {
    try {
        if (tgBot && !isShuttingDown) {
            await tgBot.sendMessage(alertChatID, message);
        }
        console.log(`🔔 ${message}`);
    } catch (error) {
        console.error('❌ Не удалось отправить алерт:', error.message);
    }
}

// ========== ЗАГРУЗКА КОНФИГОВ ==========
async function loadBotsConfig() {
    try {
        if (!existsSync(botsPath)) {
            await sendAlert(`❌ Файл ${botsPath} не найден`);
            process.exit(1);
        }
        
        const botsJson = await readFile(botsPath, 'utf-8');
        let loadedBots;
        try {
            loadedBots = JSON.parse(botsJson);
        } catch (e) {
            await sendAlert(`❌ Ошибка парсинга ${botsPath}: ${e.message}`);
            process.exit(1);
        }
        
        if (!Array.isArray(loadedBots)) {
            await sendAlert(`❌ ${botsPath} должен содержать массив`);
            process.exit(1);
        }
        
        bots.clear();
        for (const bot of loadedBots) {
            bots.set(bot.username, {
                ...bot,
                itemPrices: items,
                msgID: 0,
                msgTime: null,
                isManualStop: false,
                success: false
            });
        }
        
        console.log(`✅ bots.json загружен (${bots.size} ботов)`);
    } catch (error) {
        await sendAlert(`❌ Ошибка загрузки ${botsPath}: ${error.message}`);
        process.exit(1);
    }
}

async function loadItemsConfig() {
    try {
        if (!existsSync(itemsPath)) {
            await sendAlert(`❌ Файл ${itemsPath} не найден`);
            process.exit(1);
        }
        
        const itemsJson = await readFile(itemsPath, 'utf-8');
        try {
            items = JSON.parse(itemsJson);
        } catch (e) {
            await sendAlert(`❌ Ошибка парсинга ${itemsPath}: ${e.message}`);
            process.exit(1);
        }
        
        if (!Array.isArray(items)) {
            await sendAlert(`❌ ${itemsPath} должен содержать массив`);
            process.exit(1);
        }
        
        console.log(`✅ items.json загружен (${items.length} предметов)`);
    } catch (error) {
        await sendAlert(`❌ Ошибка загрузки ${itemsPath}: ${error.message}`);
        process.exit(1);
    }
}

// ========== РАБОТА С ВОРКЕРАМИ ==========
function safePostMessage(username, message) {
    const workerData = workers.get(username);
    if (!workerData || !workerData.worker) return false;
    
    try {
        if (!workerData.worker.terminated) {
            workerData.worker.postMessage(message);
            return true;
        }
    } catch (error) {
        // Игнорируем
    }
    return false;
}

async function runWorker(bot) {
    const username = bot.username;
    
    // Убиваем старый воркер если есть
    const existing = workers.get(username);
    if (existing) {
        if (existing.timeoutId) clearTimeout(existing.timeoutId);
        try { existing.worker.terminate(); } catch (e) {}
        workers.delete(username);
    }

    return new Promise((resolve) => {
        try {
            const workerScriptPath = join(__dirname, `${bot.type}.mjs`);
            
            if (!existsSync(workerScriptPath)) {
                console.error(`❌ Файл воркера не найден: ${workerScriptPath}`);
                resolve(null);
                return;
            }
            
            const worker = new Worker(workerScriptPath, {
                workerData: bot,
                resourceLimits: {
                    maxOldGenerationSizeMb: 200,
                }
            });

            bot.isManualStop = false;
            
            const timeoutId = setTimeout(() => {
                if (!bot.success) {
                    console.warn(`⏱ ${username} не ответил за 30 сек`);
                    try { worker.terminate(); } catch (e) {}
                }
            }, 30000);
            
            workers.set(username, { worker, timeoutId });

            worker.on('message', async (message) => {
                try {
                    if (message.name === 'success') {
                        const botToUpdate = bots.get(username);
                        if (botToUpdate) {
                            botToUpdate.success = true;
                            console.log(`✅ ${username} запущен`);
                        }
                    } else if (message.name === "buy" || message.name === "sell" || message.name === "try-sell") {
                        if (socket && isSocketOpen) {
                            const action = message.name === 'try-sell' ? 'try-sell' : message.name;
                            const payload = { action, type: message.id };
                            if (message.price) payload.price = message.price;
                            socket.send(JSON.stringify(payload));
                        }
                    } else if (message.name === "items") {
                        botItems.set(username, message.items);
                    } else if (message.name === "inventory") {
                        botInventory.set(username, message.data);
                    } else if (message.name === "buying") {
                        const updatedBuying = [...itemsBuying, message.data];
                        for (const [user, _] of workers) {
                            safePostMessage(user, { type: 'items_buying', data: updatedBuying });
                        }
                        itemsBuying = updatedBuying;
                        if (socket && isSocketOpen) {
                            socket.send(JSON.stringify({ action: "add", json_data: message.data }));
                        }
                    } else if (message.name === "set_min_price" || message.name === "set_max_price") {
                        if (socket && isSocketOpen) {
                            socket.send(JSON.stringify({ 
                                action: message.name === "set_min_price" ? 'set_min_price' : 'set_max_price', 
                                type: message.type, 
                                price: message.price 
                            }));
                        }
                    } else if (typeof message === 'string') {
                        // Любая строка от воркера отправляется в Telegram
                        await sendAlert(`📝 ${message}`);
                    }
                } catch (error) {
                    await sendAlert(`❌ Ошибка в обработчике ${username}: ${error.message}`);
                }
            });

            worker.on('error', async (error) => {
                bot.success = false;
                // Не отправляем в Telegram, просто логируем в консоль
                await sendAlert(error.message)
            });

            worker.on('exit', (code) => {
                bot.success = false;
                console.warn(`⚠️ ${username} завершился с кодом ${code}`);
                
                const workerData = workers.get(username);
                if (workerData && workerData.timeoutId) {
                    clearTimeout(workerData.timeoutId);
                }
                workers.delete(username);
                
                if (!bot.isManualStop && !isShuttingDown) {
                    setTimeout(() => {
                        console.log(`🔁 Перезапуск ${username}`);
                        runWorker(bot);
                    }, 60000);
                }
            });

            resolve(worker);
        } catch (error) {
            console.error(`❌ Ошибка запуска ${username}:`, error.message);
            resolve(null);
        }
    });
}

async function stopWorkers() {
    for (const bot of bots.values()) {
        bot.isManualStop = true;
    }
    
    for (const { worker } of workers.values()) {
        try { worker.terminate(); } catch (e) {}
    }
    workers.clear();
    console.log('✅ Все боты остановлены');
}

async function startBots() {
    try {
        await loadBotsConfig();
        await loadItemsConfig();
        
        for (const bot of bots.values()) {
            bot.itemPrices = items;
            await runWorker(bot);
        }
        
        setTimeout(() => {
            if (socket && isSocketOpen) {
                socket.send(JSON.stringify({ action: "info" }));
            }
        }, 1000);
    } catch (error) {
        await sendAlert(`❌ Ошибка запуска ботов: ${error.message}`);
    }
}

async function restartBots() {
    console.log('🔄 Перезапуск...');
    await stopWorkers();
    await startBots();
}

// ========== TELEGRAM КОМАНДЫ ==========
async function initTelegram() {
    tgBot = new TelegramBot(token, { polling: true });
    
    tgBot.onText(/\/update/, async (msg) => {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, '🔄 Обновление, перезапуск...');
        isShuttingDown = true;
        await stopWorkers();
        exec('git pull', async (err, stdout) => {
            if (err) {
                await sendAlert(`❌ Git pull error: ${err.message}`);
            } else {
                console.log('Git pull выполнен:', stdout);
            }
            process.exit(0);
        });
    });
    
    tgBot.onText(/\/ping/, async (msg) => {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, `✅ Работает (ботов: ${workers.size})`);
    });
    
    tgBot.onText(/\/start/, async (msg) => {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, '🔄 Запуск ботов');
        await startBots();
    });
    
    tgBot.onText(/\/stop/, async (msg) => {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, '⏹ Остановка');
        await stopWorkers();
    });
    
    tgBot.onText(/\/reload/, async (msg) => {
        if ((Date.now() / 1000) - msg.date > 10) return;
        await tgBot.sendMessage(alertChatID, '🔄 Перезагрузка конфигов, перезапуск...');
        isShuttingDown = true;
        await stopWorkers();
        process.exit(0);
    });
    
    console.log('✅ Telegram бот готов');
}

// ========== WEBSOCKET ==========
function connectWebSocket() {
    if (socket) {
        try { socket.close(); } catch (e) {}
    }
    
    try {
        socket = new WebSocket(WEBSOCKET_URL);

        socket.on('open', () => {
            console.log('✅ WebSocket подключен');
            isSocketOpen = true;
            socket.send(JSON.stringify({ action: "info" }));
        });

        socket.on('message', async (data) => {
            try {
                const dataObj = JSON.parse(data);
                
                if (dataObj.action === "json_update" && Array.isArray(dataObj.data)) {
                    for (const [username, _] of workers) {
                        safePostMessage(username, { type: 'items_buying', data: dataObj.data });
                    }
                    itemsBuying = dataObj.data;
                } else if (dataObj.prices) {
                    let freshItems = [];
                    try {
                        if (existsSync(itemsPath)) {
                            const itemsJson = await readFile(itemsPath, 'utf-8');
                            freshItems = JSON.parse(itemsJson);
                        } else {
                            await sendAlert(`❌ ${itemsPath} пропал при обновлении цен`);
                            process.exit(1);
                        }
                    } catch (e) {
                        await sendAlert(`❌ Ошибка чтения ${itemsPath}: ${e.message}`);
                        process.exit(1);
                    }
                    
                    const itemsWithPrice = freshItems
                        .map(item => ({
                            ...item,
                            priceSell: dataObj.prices[item.id],
                            ratio: dataObj.ratios?.[item.id] || item.ratio || 0.8
                        }))
                        .filter(item => item.priceSell !== undefined);
                    
                    items = itemsWithPrice;
                    
                    for (const bot of bots.values()) {
                        bot.itemPrices = items;
                    }
                    
                    for (const [username, _] of workers) {
                        safePostMessage(username, { type: 'price', data: items });
                    }

                    if (!botsStarted && items.length > 0) {
                        botsStarted = true;
                        startBots();
                    }
                }
            } catch (e) {
                await sendAlert(`❌ Ошибка обработки WebSocket сообщения: ${e.message}`);
            }
        });

        socket.on('close', () => {
            console.log('❌ WebSocket отключен');
            isSocketOpen = false;
            setTimeout(connectWebSocket, 5000);
        });

        socket.on('error', async (err) => {
            await sendAlert(`❌ WebSocket error: ${err.message}`);
        });
    } catch (error) {
        sendAlert(`❌ WebSocket connection error: ${error.message}`);
        setTimeout(connectWebSocket, 5000);
    }
}

// ========== МОНИТОРИНГ ==========
setInterval(() => {
    try {
        if (socket && isSocketOpen) {
            const itemsCount = new Map();
            const itemsCountInventory = new Map();
            
            for (let itemsList of botItems.values()) {
                for (let item of itemsList) {
                    itemsCount.set(item, (itemsCount.get(item) || 0) + 1);
                }
            }
            
            for (let itemsList of botInventory.values()) {
                for (let item of itemsList) {
                    itemsCountInventory.set(item, (itemsCountInventory.get(item) || 0) + 1);
                }
            }
            
            socket.send(JSON.stringify({ 
                action: "presence", 
                items: Object.fromEntries(itemsCount),
                inventory: Object.fromEntries(itemsCountInventory)
            }));
        }
    } catch (error) {
        console.error('Presence error:', error.message);
    }
}, 30000);

// Очистка лога раз в 5 часов
setInterval(async () => {
    try {
        exec('> bot.log', (err) => {
            if (err) console.error('Clean log error:', err.message);
        });
    } catch (error) {}
}, 5 * 60 * 60 * 1000);

// ========== ГЛОБАЛЬНЫЕ ОБРАБОТЧИКИ ==========
process.on('unhandledRejection', async (reason) => {
    await sendAlert(`❌ Unhandled Rejection: ${reason?.message || reason}`);
});

process.on('uncaughtException', async (error) => {
    await sendAlert(`❌ Uncaught Exception: ${error.message}`);
    setTimeout(() => {
        if (!isShuttingDown) {
            process.exit(1);
        }
    }, 3000);
});

// ========== ЗАПУСК ==========
async function main() {
    await initTelegram();
    await loadItemsConfig();
    await loadBotsConfig();
    connectWebSocket();
}

main().catch(async (error) => {
    await sendAlert(`❌ Критическая ошибка при запуске: ${error.message}`);
    process.exit(1);
});