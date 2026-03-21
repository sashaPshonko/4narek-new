import { Worker } from 'worker_threads';
import { readFile, writeFile, access } from 'fs/promises';
import { existsSync, constants } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import TelegramBot from 'node-telegram-bot-api';
import WebSocket from 'ws';
import { exec } from 'child_process';
import { promisify } from 'util';

const execPromise = promisify(exec);
const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// ========== КОНСТАНТЫ ==========
const PATHS = {
    items: join(__dirname, 'items.json'),
    bots: join(__dirname, '502.json'),
    log: join(__dirname, 'bot.log')
};

const TELEGRAM = {
    token: '8053905786:AAEN59tDF_yvC8G5Qw7TEgVOdhZ122bby1I',
    chatID: -1003827870631
};

const WEBSOCKET_URL = 'ws://85.198.86.42:8080/ws';
const WORKER_TIMEOUT = 30000;
const RESTART_DELAY = 60000;
const MAX_RESTARTS = 10;
const RESTART_WINDOW = 3600000; // 1 час
const PRESENCE_INTERVAL = 30000;
const CLEAN_LOG_INTERVAL = 5 * 60 * 60 * 1000;

// ========== ГЛОБАЛЬНЫЕ СОСТОЯНИЯ ==========
let items = [];
let bots = [];
let workers = new Map(); // username -> { worker, restartCount, lastRestart, timeoutId }
let botItems = new Map();
let botInventory = new Map();
let itemsBuying = [];
let socket = null;
let isSocketOpen = false;
let isShuttingDown = false;
let isStartingBots = false;
let healthCheckInterval = null;
let reconnectTimeout = null;
let telegramBot = null;

// ========== УНИВЕРСАЛЬНЫЙ ЛОГГЕР ==========
class Logger {
    static async error(error, context = '', extraData = null) {
        const timestamp = new Date().toLocaleString('ru-RU');
        const errorMessage = error?.message || String(error);
        const errorStack = error?.stack ? `\n📚 Stack: ${error.stack.substring(0, 500)}` : '';
        
        const fullMessage = `❌ [${timestamp}] ${context}\n📝 ${errorMessage}${errorStack}`;
        console.error(fullMessage);
        
        if (extraData) {
            console.error('📦 Дополнительные данные:', extraData);
        }
        
        // Асинхронная отправка в Telegram без ожидания
        if (telegramBot && !isShuttingDown) {
            try {
                const truncatedMessage = fullMessage.substring(0, 4000);
                await telegramBot.sendMessage(TELEGRAM.chatID, truncatedMessage);
            } catch (tgError) {
                console.error('❌ Не удалось отправить ошибку в Telegram:', tgError.message);
            }
        }
    }
    
    static async info(message) {
        const timestamp = new Date().toLocaleString('ru-RU');
        const formattedMessage = `✅ [${timestamp}] ${message}`;
        console.log(formattedMessage);
        
        if (telegramBot && !isShuttingDown && Math.random() < 0.1) { // Не спамим
            try {
                await telegramBot.sendMessage(TELEGRAM.chatID, formattedMessage);
            } catch (e) {
                // Игнорируем ошибки отправки информационных сообщений
            }
        }
    }
    
    static async warning(message) {
        const timestamp = new Date().toLocaleString('ru-RU');
        const formattedMessage = `⚠️ [${timestamp}] ${message}`;
        console.warn(formattedMessage);
    }
}

// ========== ПРОВЕРКА ФАЙЛОВ ==========
async function ensureFileExists(filePath, defaultContent = '[]') {
    try {
        await access(filePath, constants.R_OK | constants.W_OK);
        return true;
    } catch (error) {
        if (error.code === 'ENOENT') {
            try {
                await writeFile(filePath, defaultContent, 'utf-8');
                await Logger.info(`Создан файл: ${filePath}`);
                return true;
            } catch (writeError) {
                await Logger.error(writeError, `Не удалось создать файл: ${filePath}`);
                return false;
            }
        }
        await Logger.error(error, `Ошибка доступа к файлу: ${filePath}`);
        return false;
    }
}

// ========== ЗАГРУЗКА КОНФИГУРАЦИЙ ==========
async function loadItemsConfig() {
    try {
        const fileExists = await ensureFileExists(PATHS.items);
        if (!fileExists) {
            items = [];
            return;
        }
        
        const itemsJson = await readFile(PATHS.items, 'utf-8');
        items = JSON.parse(itemsJson);
        await Logger.info(`items.json загружен (${items.length} предметов)`);
    } catch (error) {
        await Logger.error(error, 'loadItemsConfig');
        items = [];
    }
}

async function loadBotsConfig() {
    try {
        const fileExists = await ensureFileExists(PATHS.bots);
        if (!fileExists) {
            bots = [];
            return;
        }
        
        const botsJson = await readFile(PATHS.bots, 'utf-8');
        const loadedBots = JSON.parse(botsJson);
        
        bots = loadedBots.map(bot => ({
            ...bot,
            itemPrices: items,
            msgID: 0,
            msgTime: null,
            isManualStop: false,
            success: false
        }));
        
        await Logger.info(`bots.json загружен (${bots.length} ботов)`);
    } catch (error) {
        await Logger.error(error, 'loadBotsConfig');
        bots = [];
    }
}

// ========== БЕЗОПАСНАЯ РАБОТА С ВОРКЕРАМИ ==========
function isWorkerAlive(worker) {
    if (!worker || typeof worker.terminate !== 'function') return false;
    try {
        return !worker.terminated && worker.threadId !== undefined;
    } catch (e) {
        return false;
    }
}

function safePostMessage(worker, message) {
    if (!worker || !isWorkerAlive(worker)) return false;
    try {
        worker.postMessage(message);
        return true;
    } catch (error) {
        Logger.error(error, 'safePostMessage', { messageType: message?.type });
        return false;
    }
}

async function safeTerminateWorker(worker, username) {
    if (!worker || !isWorkerAlive(worker)) return;
    
    try {
        const timeoutPromise = new Promise((_, reject) => {
            setTimeout(() => reject(new Error('Terminate timeout')), 5000);
        });
        
        const terminatePromise = worker.terminate();
        await Promise.race([terminatePromise, timeoutPromise]);
        await Logger.info(`Воркер ${username} остановлен`);
    } catch (error) {
        await Logger.error(error, `Ошибка при остановке воркера ${username}`);
        // Принудительное удаление
        if (worker.threadId) {
            try { process.kill(worker.threadId); } catch (e) {}
        }
    }
}

// ========== ЗАПУСК ВОРКЕРА С ЗАЩИТОЙ ==========
async function runWorker(bot) {
    const username = bot.username;
    
    // Проверяем существование файла воркера
    const workerScriptPath = join(__dirname, `${bot.type}.mjs`);
    if (!existsSync(workerScriptPath)) {
        await Logger.error(new Error(`Worker file not found: ${workerScriptPath}`), 'runWorker', { username });
        return null;
    }
    
    // Останавливаем старый воркер если есть
    const existing = workers.get(username);
    if (existing) {
        if (existing.timeoutId) clearTimeout(existing.timeoutId);
        await safeTerminateWorker(existing.worker, username);
        workers.delete(username);
    }
    
    // Проверяем лимит рестартов
    const restartData = workers.get(`${username}_restart`) || { count: 0, firstRestart: Date.now() };
    const now = Date.now();
    
    if (now - restartData.firstRestart > RESTART_WINDOW) {
        // Сброс счетчика если прошло достаточно времени
        restartData.count = 0;
        restartData.firstRestart = now;
    }
    
    if (restartData.count >= MAX_RESTARTS && !bot.isManualStop) {
        await Logger.error(new Error(`Too many restarts`), 'runWorker', { username, count: restartData.count });
        await Logger.info(`❌ Бот ${username} остановлен из-за слишком частых рестартов`);
        return null;
    }
    
    return new Promise((resolve) => {
        try {
            const worker = new Worker(workerScriptPath, {
                workerData: { ...bot, itemPrices: items },
                resourceLimits: {
                    maxOldGenerationSizeMb: 200,
                }
            });
            
            const timeoutId = setTimeout(() => {
                if (workers.get(username)?.worker === worker && !bot.success) {
                    Logger.warning(`⏱ ${username} не ответил успехом за ${WORKER_TIMEOUT/1000} сек.`);
                    safeTerminateWorker(worker, username);
                }
            }, WORKER_TIMEOUT);
            
            worker.on('message', async (message) => {
                try {
                    await handleWorkerMessage(worker, username, bot, message);
                } catch (error) {
                    await Logger.error(error, 'worker_message_handler', { username, messageType: message?.name });
                }
            });
            
            worker.on('error', async (error) => {
                await Logger.error(error, 'worker_error', { username });
                bot.success = false;
            });
            
            worker.on('exit', async (code) => {
                bot.success = false;
                await Logger.warning(`Worker ${username} завершился с кодом ${code}`);
                
                const workerData = workers.get(username);
                if (workerData && workerData.worker === worker) {
                    if (workerData.timeoutId) clearTimeout(workerData.timeoutId);
                    
                    if (!bot.isManualStop && !isShuttingDown) {
                        // Увеличиваем счетчик рестартов
                        const restartInfo = workers.get(`${username}_restart`) || { count: 0, firstRestart: Date.now() };
                        restartInfo.count++;
                        workers.set(`${username}_restart`, restartInfo);
                        
                        setTimeout(() => {
                            if (!isShuttingDown && !bot.isManualStop) {
                                Logger.info(`🔁 Перезапуск бота ${username} через ${RESTART_DELAY/1000} сек`);
                                runWorker(bot);
                            }
                        }, RESTART_DELAY);
                    }
                    
                    workers.delete(username);
                }
            });
            
            workers.set(username, { worker, timeoutId, restartCount: restartData.count, lastRestart: Date.now() });
            resolve(worker);
            
        } catch (error) {
            Logger.error(error, 'runWorker_create', { username });
            resolve(null);
        }
    });
}

// ========== ОБРАБОТЧИК СООБЩЕНИЙ ОТ ВОРКЕРОВ ==========
async function handleWorkerMessage(worker, username, bot, message) {
    if (message.name === 'success') {
        const botToUpdate = bots.find(b => b.username === username);
        if (botToUpdate) {
            botToUpdate.success = true;
            await Logger.info(`${username} успешно запущен`);
        }
        // Сбрасываем счетчик рестартов при успешном запуске
        workers.delete(`${username}_restart`);
    }
    else if (message.name === 'buy' || message.name === 'sell' || message.name === 'try-sell') {
        if (socket && isSocketOpen) {
            const action = message.name === 'try-sell' ? 'try-sell' : message.name;
            const payload = { action, type: message.id };
            if (message.price) payload.price = message.price;
            safeSocketSend(payload);
        }
    }
    else if (message.name === 'items') {
        botItems.set(username, message.items);
    }
    else if (message.name === 'inventory') {
        botInventory.set(username, message.data);
    }
    else if (message.name === 'buying') {
        await broadcastBuyingLocally(message.data);
        safeSocketSend({ action: 'add', json_data: message.data });
    }
    else if (message.name === 'set_min_price') {
        safeSocketSend({ action: 'set_min_price', type: message.type, price: message.price });
        await Logger.info(`📉 Установка минимальной цены для ${message.type}: ${message.price}`);
    }
    else if (message.name === 'set_max_price') {
        safeSocketSend({ action: 'set_max_price', type: message.type, price: message.price });
        await Logger.info(`📈 Установка максимальной цены для ${message.type}: ${message.price}`);
    }
    else if (typeof message === 'string') {
        if (message.includes('ввести капчу')) {
            await Logger.warning(`⚠️ Капча: ${message}`);
        }
        if (telegramBot) {
            await telegramBot.sendMessage(TELEGRAM.chatID, `📝 ${message}`);
        }
    }
}

// ========== БЕЗОПАСНАЯ РАБОТА С WEBSOCKET ==========
function safeSocketSend(data) {
    if (!socket || socket.readyState !== WebSocket.OPEN) return false;
    try {
        socket.send(JSON.stringify(data));
        return true;
    } catch (error) {
        Logger.error(error, 'safeSocketSend', { data: JSON.stringify(data).substring(0, 200) });
        return false;
    }
}

// ========== ЛОКАЛЬНАЯ СИНХРОНИЗАЦИЯ ==========
async function broadcastBuyingLocally(uuid) {
    try {
        const updatedBuying = [...itemsBuying, uuid];
        
        for (const [username, { worker }] of workers) {
            safePostMessage(worker, { type: 'items_buying', data: updatedBuying });
        }
        
        itemsBuying = updatedBuying;
    } catch (error) {
        await Logger.error(error, 'broadcastBuyingLocally', { uuid });
    }
}

// ========== ОСТАНОВКА ВСЕХ БОТОВ ==========
async function stopWorkers() {
    await Logger.info('Остановка всех ботов...');
    
    // Устанавливаем флаг ручной остановки
    bots.forEach(bot => { bot.isManualStop = true; });
    
    const stopPromises = [];
    for (const [username, { worker, timeoutId }] of workers) {
        if (timeoutId) clearTimeout(timeoutId);
        stopPromises.push(safeTerminateWorker(worker, username));
    }
    
    await Promise.allSettled(stopPromises);
    workers.clear();
    botItems.clear();
    botInventory.clear();
    itemsBuying = [];
    
    await Logger.info('Все боты остановлены');
}

// ========== ОЧИСТКА ЛОГА ==========
async function cleanNPM() {
    try {
        await execPromise(`> ${PATHS.log}`);
        await Logger.info(`bot.log очищен`);
    } catch (error) {
        await Logger.error(error, 'cleanNPM');
    }
}

// ========== GIT PULL ==========
async function gitPull() {
    try {
        const { stdout, stderr } = await execPromise('git pull');
        if (stderr) await Logger.warning(`Git pull stderr: ${stderr}`);
        return stdout;
    } catch (error) {
        await Logger.error(error, 'gitPull');
        throw error;
    }
}

// ========== ЗАПУСК БОТОВ ==========
async function startBots() {
    if (isStartingBots) {
        await Logger.warning('Боты уже запускаются, пропускаем');
        return;
    }
    
    isStartingBots = true;
    try {
        await loadBotsConfig();
        await loadItemsConfig();
        
        // Обновляем цены у ботов
        bots.forEach(bot => bot.itemPrices = items);
        
        const botPromises = bots.map(bot => runWorker(bot));
        const results = await Promise.allSettled(botPromises);
        
        const successCount = results.filter(r => r.status === 'fulfilled' && r.value !== null).length;
        await Logger.info(`Запущено ботов: ${successCount}/${bots.length}`);
        
        // Отправляем info через WebSocket
        setTimeout(() => safeSocketSend({ action: 'info' }), 3000);
    } catch (error) {
        await Logger.error(error, 'startBots');
    } finally {
        isStartingBots = false;
    }
}

async function restartBots() {
    await Logger.info('🔄 Перезапуск всех ботов...');
    await stopWorkers();
    await startBots();
}

// ========== МОНИТОРИНГ ЗДОРОВЬЯ ==========
function startHealthCheck() {
    if (healthCheckInterval) clearInterval(healthCheckInterval);
    
    healthCheckInterval = setInterval(async () => {
        try {
            // Проверяем WebSocket
            if (!socket || socket.readyState !== WebSocket.OPEN) {
                await Logger.warning('WebSocket не в OPEN состоянии, попытка переподключения...');
                if (reconnectTimeout) clearTimeout(reconnectTimeout);
                reconnectTimeout = setTimeout(connectWebSocket, 5000);
            }
            
            // Проверяем воркеров
            let activeWorkers = 0;
            for (const [username, { worker }] of workers) {
                if (isWorkerAlive(worker)) {
                    activeWorkers++;
                } else {
                    workers.delete(username);
                }
            }
            
            // Отправляем присутствие
            if (socket && socket.readyState === WebSocket.OPEN) {
                const itemsCount = new Map();
                const itemsCountInventory = new Map();
                
                for (const itemsList of botItems.values()) {
                    for (const item of itemsList) {
                        itemsCount.set(item, (itemsCount.get(item) || 0) + 1);
                    }
                }
                
                for (const itemsList of botInventory.values()) {
                    for (const item of itemsList) {
                        itemsCountInventory.set(item, (itemsCountInventory.get(item) || 0) + 1);
                    }
                }
                
                safeSocketSend({
                    action: 'presence',
                    items: Object.fromEntries(itemsCount),
                    inventory: Object.fromEntries(itemsCountInventory)
                });
            }
        } catch (error) {
            await Logger.error(error, 'healthCheck');
        }
    }, PRESENCE_INTERVAL);
}

// ========== WEBSOCKET ПОДКЛЮЧЕНИЕ ==========
async function connectWebSocket() {
    if (reconnectTimeout) clearTimeout(reconnectTimeout);
    
    try {
        if (socket) {
            try { socket.close(); } catch (e) {}
            socket = null;
        }
        
        socket = new WebSocket(WEBSOCKET_URL);
        
        socket.on('open', async () => {
            await Logger.info('✅ Подключено к WebSocket серверу');
            isSocketOpen = true;
            safeSocketSend({ action: 'info' });
        });
        
        socket.on('message', async (data) => {
            try {
                const dataObj = JSON.parse(data);
                
                if (dataObj.action === 'json_update' && Array.isArray(dataObj.data)) {
                    // Обновление списка покупок
                    for (const [_, { worker }] of workers) {
                        safePostMessage(worker, { type: 'items_buying', data: dataObj.data });
                    }
                    itemsBuying = dataObj.data;
                }
                else if (dataObj.prices) {
                    await updatePrices(dataObj);
                }
            } catch (error) {
                await Logger.error(error, 'websocket_message');
            }
        });
        
        socket.on('close', async () => {
            await Logger.warning('❌ WebSocket отключён');
            isSocketOpen = false;
            reconnectTimeout = setTimeout(connectWebSocket, 5000);
        });
        
        socket.on('error', async (err) => {
            await Logger.error(err, 'WebSocket_error');
        });
        
    } catch (error) {
        await Logger.error(error, 'connectWebSocket');
        reconnectTimeout = setTimeout(connectWebSocket, 5000);
    }
}

// ========== ОБНОВЛЕНИЕ ЦЕН ==========
async function updatePrices(dataObj) {
    try {
        let freshItems = [];
        const fileExists = await ensureFileExists(PATHS.items);
        
        if (fileExists) {
            const itemsJson = await readFile(PATHS.items, 'utf-8');
            freshItems = JSON.parse(itemsJson);
        }
        
        const itemsWithPrice = freshItems
            .map(item => ({
                ...item,
                priceSell: dataObj.prices[item.id],
                ratio: dataObj.ratios?.[item.id] || item.ratio || 0.8
            }))
            .filter(item => item.priceSell !== undefined && item.priceSell !== null);
        
        items = itemsWithPrice;
        
        // Обновляем цены у всех ботов
        for (const [_, { worker }] of workers) {
            safePostMessage(worker, { type: 'price', data: items });
        }
        
        // Обновляем itemPrices у объектов ботов
        bots.forEach(bot => bot.itemPrices = items);
        
        await Logger.info(`Обновлены цены для ${items.length} предметов`);
        
        // Запускаем ботов если еще не запущены
        if (!isStartingBots && workers.size === 0 && items.length > 0) {
            await startBots();
        }
    } catch (error) {
        await Logger.error(error, 'updatePrices');
    }
}

// ========== TELEGRAM КОМАНДЫ ==========
async function initTelegramBot() {
    try {
        telegramBot = new TelegramBot(TELEGRAM.token, { polling: true });
        
        const commands = [
            { cmd: '/update', desc: 'Git pull и перезапуск' },
            { cmd: '/ping', desc: 'Проверка работы' },
            { cmd: '/start', desc: 'Запуск ботов' },
            { cmd: '/stop', desc: 'Остановка ботов' },
            { cmd: '/reload', desc: 'Перезагрузка конфигурации' },
            { cmd: '/status', desc: 'Статус ботов' }
        ];
        
        try {
            await telegramBot.setMyCommands(commands.map(c => ({ command: c.cmd, description: c.desc })));
        } catch (e) {}
        
        telegramBot.onText(/\/update/, async (msg) => {
            if ((Date.now() / 1000) - msg.date > 10) return;
            await telegramBot.sendMessage(TELEGRAM.chatID, '🔄 Обновление через git pull...');
            await stopWorkers();
            const pullResult = await gitPull();
            await telegramBot.sendMessage(TELEGRAM.chatID, `✅ Git pull выполнен:\n${pullResult.substring(0, 1000)}`);
            process.exit(0);
        });
        
        telegramBot.onText(/\/ping/, async (msg) => {
            if ((Date.now() / 1000) - msg.date > 10) return;
            await telegramBot.sendMessage(TELEGRAM.chatID, `✅ Работает\nБотов: ${workers.size}\nWebSocket: ${isSocketOpen ? '✅' : '❌'}`);
        });
        
        telegramBot.onText(/\/start/, async (msg) => {
            if ((Date.now() / 1000) - msg.date > 10) return;
            await telegramBot.sendMessage(TELEGRAM.chatID, '🔄 Запуск ботов');
            await startBots();
        });
        
        telegramBot.onText(/\/stop/, async (msg) => {
            if ((Date.now() / 1000) - msg.date > 10) return;
            await telegramBot.sendMessage(TELEGRAM.chatID, '⏹ Остановка ботов');
            await stopWorkers();
        });
        
        telegramBot.onText(/\/reload/, async (msg) => {
            if ((Date.now() / 1000) - msg.date > 10) return;
            await telegramBot.sendMessage(TELEGRAM.chatID, '🔄 Перезагрузка конфигурации...');
            await restartBots();
            await telegramBot.sendMessage(TELEGRAM.chatID, '✅ Конфигурация перезагружена');
        });
        
        telegramBot.onText(/\/status/, async (msg) => {
            if ((Date.now() / 1000) - msg.date > 10) return;
            let status = `📊 Статус ботов:\n`;
            status += `Активных: ${workers.size}/${bots.length}\n`;
            status += `WebSocket: ${isSocketOpen ? '✅' : '❌'}\n`;
            status += `Предметов: ${items.length}\n`;
            status += `В очереди покупки: ${itemsBuying.length}`;
            await telegramBot.sendMessage(TELEGRAM.chatID, status);
        });
        
        await Logger.info('Telegram бот инициализирован');
    } catch (error) {
        await Logger.error(error, 'initTelegramBot');
    }
}

// ========== GRACEFUL SHUTDOWN ==========
async function gracefulShutdown(signal) {
    if (isShuttingDown) return;
    isShuttingDown = true;
    
    await Logger.info(`\n🛑 Получен сигнал ${signal}, graceful shutdown...`);
    
    if (healthCheckInterval) clearInterval(healthCheckInterval);
    if (reconnectTimeout) clearTimeout(reconnectTimeout);
    
    await stopWorkers();
    
    if (socket) {
        try { socket.close(); } catch (e) {}
        socket = null;
    }
    
    if (telegramBot) {
        try { telegramBot.stopPolling(); } catch (e) {}
    }
    
    await Logger.info('✅ Graceful shutdown завершен');
    process.exit(0);
}

// ========== ГЛОБАЛЬНЫЕ ОБРАБОТЧИКИ ==========
process.on('unhandledRejection', async (reason) => {
    await Logger.error(reason, 'unhandledRejection');
});

process.on('uncaughtException', async (error) => {
    await Logger.error(error, 'uncaughtException');
    // Не выходим сразу, даем время отправить сообщение
    setTimeout(() => {
        if (!isShuttingDown) {
            gracefulShutdown('uncaughtException');
        }
    }, 3000);
});

process.on('SIGTERM', () => gracefulShutdown('SIGTERM'));
process.on('SIGINT', () => gracefulShutdown('SIGINT'));

// ========== ИНИЦИАЛИЗАЦИЯ ==========
async function main() {
    try {
        await Logger.info('🚀 Запуск основного приложения');
        
        // Инициализация
        await loadItemsConfig();
        await loadBotsConfig();
        await initTelegramBot();
        
        // Запуск WebSocket
        connectWebSocket();
        
        // Запуск мониторинга
        startHealthCheck();
        
        // Периодическая очистка лога
        setInterval(async () => {
            try {
                await cleanNPM();
            } catch (error) {
                await Logger.error(error, 'auto_clean_log');
            }
        }, CLEAN_LOG_INTERVAL);
        
        await Logger.info('✅ Приложение успешно запущено');
    } catch (error) {
        await Logger.error(error, 'main');
        setTimeout(main, 5000);
    }
}

// Запуск
main().catch(console.error);