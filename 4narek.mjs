import fs from 'fs/promises';
import mineflayer from 'mineflayer';
import inventoryViewer from 'mineflayer-web-inventory';
import { createLogger, transports, format } from 'winston';
import { workerData, parentPort } from 'worker_threads';
import { loader as autoEat } from 'mineflayer-auto-eat'
import { writeFile, rename } from 'fs/promises';
import { join } from 'path';
import net from 'net';
import { generateKey } from 'crypto';

let itemPrices = workerData.itemPrices
let itemsBuying = []
let needReset = false
let netakbistro = true
parentPort.on('message', (data) => {
    if (data.type === 'price') {
        needReset = true
        itemPrices = data.data
    }
    if (data.type === 'items_buying') {
        itemsBuying = data.data
    }
});

const minDelay = 500;
const AHDelay = 2000;
const loadingDelay = 100;

const chooseBuying = 'Выбор скупки ресурсов';
const setSectionFarmer = 'Установка секции "фермер"';
const sectionFarmer = 'Секция "фермер"';
const setSectionFood = 'Установка секции "еда"';
const sectionFood = 'Секция "еда"';
const setSectionResources = 'Установка секции "ценные ресурсы"';
const sectionResources = 'Секция "ценные ресурсы"';
const setSectionLoot = 'Установка секции "добыча"';
const sectionLoot = 'Секция "добыча"';
const analysisAH = 'Анализ аукциона';
const buy = 'Покупка';
const myItems = 'Хранилище';
const setAH = 'Установка аукциона';

const slotToChooseBuying = 13;
const slotToSetSectionFarmer = 13;
const slotToLeaveSection = 3;
const slotToSetSectionFood = 21;
const slotToSetSectionResources = 23;
const slotToSetSectionLoot = 31;
const slotToTuneAH = 52;
const slotToReloadAH = 49;
const slotToTryBuying = 0;

const ahCommand = `/ah search ${workerData.item}`

let mu = false

let type = ""

const missingEnchantsNames = ["minecraft:knockback", "heavy", "unstable", "minecraft:thorns", "minecraft:binding_curse"]

const minBalance = 10000000

const leftMouseButton = 0;
const noShift = 0;
const firstInventorySlot = 9;
const lastInventorySlot = 44;
const firstAHSlot = 0;
const lastAHSlot = 44;
const firstSellSlot = 36;

const logger = createLogger({
    level: 'info',
    format: format.combine(
        format.colorize(),
        format.timestamp(),
        format.printf(({ timestamp, level, message }) => {
            return `${timestamp} ${level}: ${message}`;
        })
    ),
    transports: [
        new transports.Console()
    ]
});


async function launchBookBuyer(name, password, anarchy) {
    let bot = {
        mu: false,
    }
    await delay(getRandomDelayInRange(0, 10000))
    bot = mineflayer.createBot({
        host: 'mc.funtime.su',
        port: 25565,
        username: name,
        password: password,
        version: '1.16.5',
        // connect: (client) => {
        //     // Создаём сокет, привязанный к IP модема
        //     const socket = net.createConnection({
        //         host: 'mc.funtime.su',
        //         port: 25565,
        //         localAddress: workerData.ip
        //     });
            
        //     socket.on('connect', () => {
        //         console.log(`✅ Бот подключён через IP ${socket.localAddress}`);
        //         client.setSocket(socket);
        //         client.emit('connect');
        //     });
            
        //     socket.on('error', (err) => {
        //         console.error('❌ Ошибка сокета:', err);
        //     });
        // },
        chatLengthLimit: 256,  // Добавь это
        viewDistance: 'tiny'    // И это
    });


    const loginCommand = `/l ${name}`;
    const anarchyCommand = `/an${anarchy}`;
    const shopCommand = '/shop';

    console.warn = () => { };

    bot.once('login', async () => {
        bot.loadPlugin(autoEat)
        bot.startTime = Date.now() - 55000;
        bot.ahFull = false;
        bot.timeReset = Date.now()
        bot.login = true;
        bot.timeActive = Date.now();
        bot.timeLogin = Date.now()
        bot.prices = []
        bot.count = 0
        netakbistro = true
        bot.ah = []
        bot.needSell = false
        bot.startClickTime = null
        bot.updateWindow = false
        setInterval(() => {
        const inv = []
        for (let i = 0; i <= lastInventorySlot; i++) {
            const slotData = bot.inventory.slots[i];
            if (!slotData) continue;
            
            const config = findMatchingConfigItem(slotData, itemPrices);
            if (config) {
                inv.push(config.id);
            }
        }
        const msg = {name: "inventory", data: inv, username: bot.username}
        parentPort.postMessage(msg)
        }, 10000)

    logger.info(`${name} успешно проник на сервер.`);
    await delay(5000);
    bot.chat(loginCommand);

    await delay(5000);
    bot.chat(anarchyCommand);

    await delay(5000);
    bot.chat(shopCommand);
    });
        bot.on('end', (reason) => {
            console.log(reason)
            process.exit(1); 
        });

        bot.on('kicked', (reason) => {
            console.log(reason)
            process.exit(1);
        });

        bot.on('error', (err) => {
            console.log(err)
            process.exit(1);
        });
bot.on('physicsTick', async () => {
    if (Date.now() - bot.timeActive > 90000) {
        mu = false
        bot.timeActive = Date.now();
        bot.menu = analysisAH
        await safeAH(bot);
    }
})

bot.menu = chooseBuying;

let slotToBuy = undefined;

bot.startTime = Date.now() - 240000;


bot.on('windowOpen', async () => {
    let key = ""
    switch (bot.menu) {
        case chooseBuying:
            const msg = { name: 'success', username: workerData.username };
            parentPort.postMessage(msg);
            await delay(3000);
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = setSectionFarmer;

            await safeClick(bot, slotToChooseBuying, minDelay);

            break;

        case setSectionFarmer:
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = sectionFarmer;

            await safeClick(bot, slotToSetSectionFarmer, minDelay);

            break;

        case sectionFarmer:
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = setSectionFood;

            await safeClick(bot, slotToLeaveSection, minDelay);

            break;

        case setSectionFood:

            logger.info(`${name} - ${bot.menu}`);
            bot.menu = sectionFood;

            await safeClick(bot, slotToSetSectionFood, minDelay);

            break;

        case sectionFood:
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = setSectionResources;

            await safeClick(bot, slotToLeaveSection, minDelay);

            break;

        case setSectionResources:
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = sectionResources;

            await delay(getRandomDelayInRange(1000, 2500));


            await safeClick(bot, slotToSetSectionResources, minDelay);

            break;

        case sectionResources:
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = setSectionLoot;

            await delay(getRandomDelayInRange(1000, 2500));


            await safeClick(bot, slotToLeaveSection, minDelay);

            break;

        case setSectionLoot:
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = sectionLoot;

            await delay(getRandomDelayInRange(1000, 2500));

            await safeClick(bot, slotToSetSectionLoot, minDelay);

            break;

        case sectionLoot:
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = analysisAH;
            await delay(5000);
            bot.closeWindow(bot.currentWindow);
            await delay(500);

            while (Date.now() - bot.timeLogin < 13000) {
                await delay(1000)
            }
            await safeAH(bot);

            break;

        case analysisAH:
            // if (workerData.item == 'netherite sword') saveToJsonFile('sword.json', bot.currentWindow.slots)
            logger.info(`${name} - ${bot.menu}`);
            bot.timeActive = Date.now();
            generateRandomKey(bot);
            key = bot.key
            const resetime = Math.floor((Date.now() - bot.timeReset) / 1000)
            if (resetime > 60 || needReset) {
                logger.info(`${name} - ресет`);
                await delay(500);
                bot.menu = myItems;
                await safeClickBuy(bot, 46, getRandomDelayInRange(700, 1300), key)

                break;
            }
            const uptime = Math.floor((Date.now() - bot.startTime) / 1000);  // Время в секундах
            if (uptime > 55 || bot.needSell) {
                logger.info(`${name} - продажа`);
                await sellItems(bot, itemPrices)

                break;
            }

            logger.info(`${name} - ${bot.menu}`);
            let count = 0
            for (let i = firstInventorySlot; i <= lastInventorySlot; i++) {
                if (bot.inventory.slots[i]) count++
            }
                    
            if (count >= 36-bot.count) {
                logger.error('Инвентарь заполнен')
                await sellItems(bot, itemPrices)

                break;
            }
            logger.info(`${name} - поиск лучшего предмета`);
            let slotToBuy = await getBestAHSlot(bot, itemPrices);

            switch (slotToBuy) {
                case null:
                    bot.menu = analysisAH;
                    await safeClickBuy(bot, slotToReloadAH, getRandomDelayInRange(1500, 4500), key);

                    break;
                default:
                    if (netakbistro) {
                        netakbistro = false;
                        await safeClickBuy(bot, slotToBuy, 1655, key);
                    } else if (slotToBuy < 9) {
                        await safeClickBuy(bot, slotToBuy, getRandomDelayInRange(100, 150)*(slotToBuy+1), key);
                    } else {
                        await safeClickBuy(bot, slotToReloadAH, getRandomDelayInRange(1500, 4500), key);
                    }
                break;
                  
            }

            break;

        case myItems:
            generateRandomKey(bot)
            
            key = bot.key
            if (bot.currentWindow.slots[27]) {
                logger.error('суки обновили аукцион')
                break
            }
            await delay(500);
            needReset = false;
            logger.info(`${name} - ${bot.menu}`);
            
            bot.count = 0;
            bot.ah = [];
            
            let slot = null;

            // 1. Проверка на необходимость сброса
            for (let i = 0; i < 8; i++) {
                const currentSlot = bot.currentWindow?.slots[i];
                if (!currentSlot) break; // Слотов больше нет

                const priceOnAH = await getBuyPriceInStorage(currentSlot);

                // ФИКС 1: Если предмет не найден в базе по этой цене (цена изменилась)
                const priceSell = await getPriceByEnchantments(currentSlot, itemPrices)


                // ФИКС 2: Проверяем, совпадает ли цена в конфиге с ценой на аукционе
                if (priceSell !== priceOnAH) {
                    logger.error(`chnge ${priceSell} ${priceOnAH}`)
                    bot.ahFull = false
                    slot = i;
                    break;
                }
            }

            // 2. Если нашли слот для сброса — кликаем и выходим
            if (slot !== null) { // Используем явную проверку на null
                bot.ahFull = false;
                bot.needSell = true;
                bot.menu = myItems;
                await safeClickBuy(bot, slot, getRandomDelayInRange(700, 1300), key);
                break;
            }

            // 3. Если сброс не нужен — считаем предметы для статистики
            for (let i = 0; i < 8; i++) {
                const currentSlot = bot.currentWindow?.slots[i];
                if (currentSlot) { 
                    bot.count++;
                    const id = getIDByEnchantments(currentSlot, itemPrices)
                    // const id = getIdBySellPrice(itemPrices, price);
                    bot.ah.push(id);
                } else {
                    break;
                }
            }

            // ФИКС 3: Правильное определение заполненности аукциона
            // Допустим, лимит — 8 слотов
            

            const msgAH = { name: 'items', username: bot.username, items: bot.ah };
            parentPort.postMessage(msgAH);

            // 4. Переход в следующее меню
            if (Math.floor((Date.now() - bot.timeReset) / 1000) > 60) {
                bot.menu = setAH;
                await safeClickBuy(bot, 52, getRandomDelayInRange(700, 1300), key);
            } else {
                bot.menu = analysisAH;
                await safeClickBuy(bot, 46, getRandomDelayInRange(700, 1300), key);
            }
            break;

        case setAH:
            generateRandomKey(bot)
            key = bot.key
            logger.info(`${name} - ${bot.menu}`);
            bot.menu = analysisAH;

            await safeClickBuy(bot, 46, getRandomDelayInRange(700, 1300), key)

            break;

        case "clan":
            logger.info(`${bot.username} ${bot.menu}`)
            generateRandomKey(bot)
    
            let countItems = countTotalItemsInWindow(bot, itemPrices)
            if (bot.ahFull && countItems === 0) {
                const slot = findFirstMatchingSlotInInventory(bot, itemPrices)
                if (slot) {
                    logger.info(`${bot.username} добавил`)
                    await safeClickBuy(bot, slot, 500, bot.key)
                    
                }
            } else if (!bot.ahFull && countItems > 0) {
                const slot = findFirstMatchingSlotInWindow(bot, itemPrices)
                if (slot) {
                    logger.info(`${bot.username} забрал`)
                    bot.needSell = true
                    await safeClickBuy(bot, slot, 500, bot.key)
                    
                }
            }
            logger.info(`${bot.username} никуда не кликнул`)
            await delay(300)
            if (bot.currentWindow) {
                bot.closeWindow(bot.currentWindow);
            }
            bot.startTime = Date.now();
            mu = false;
            logger.info(`${bot.username} - мьютекс снят`);

            await delay(500);
            bot.menu = analysisAH;
            await safeAH(bot);
            break
    }
});

bot.on('message', async (message) => {
    const messageText = message.toString();
    console.log(messageText)

    if (messageText.includes('[☃] Вы успешно купили')) {
        bot.needSell = true
         let balanceStr = messageText
        if (messageText.includes('.')) {
            balanceStr = balanceStr.slice(0, -3)
        }
        balanceStr = balanceStr.replace(/\D/g, '')
        const balance = parseInt(balanceStr);
        const msg = { name: 'buy', id: bot.type, price: balance }
        parentPort.postMessage(msg);
        return
    }//[✘] Ошибка! По такой цене
    //BotFilter >> Введите номер с картинки в чат

    if (messageText.includes('BotFilter >> Введите номер с картинки в чат')) {
        parentPort.postMessage(`${workerData.username} - ввести капчу`)
        return
    }

    if (messageText.includes('вы забанены')) {
        parentPort.postMessage(`${workerData.username} - забанен`)
        return
    }

    if (messageText.includes('[✘] Ошибка! По такой цене')) {
        console.log('[✘] Ошибка! По такой цене ', workerData.itemID)
        return
    }


    if (messageText.includes('[✘] Ошибка! Этот товар уже Купили!')) {
        await safeClick(bot, slotToReloadAH, getRandomDelayInRange(1500, 3000))
        return
    }

    if (messageText.includes('Сервер заполнен')) {
        mu = false;
        bot.startTime = Date.now() - 240000;
        bot.ahFull = false;
        bot.timeReset = Date.now() - 60000;
        bot.login = true;
        bot.timeActive = Date.now();
        bot.timeLogin = Date.now()
        bot.prices = []
        bot.count = 0
        netakbistro = true

        await delay(minDelay);
        bot.chat(anarchyCommand);
    }

    if (messageText.includes('[☃] У Вас купили')) {
        bot.ahFull = false;
        let balanceStr = messageText
        if (messageText.includes('.')) {
            balanceStr = balanceStr.slice(0, -3)
        }
        balanceStr = balanceStr.replace(/\D/g, '')
        const balance = parseInt(balanceStr);
        const id = getIdBySellPrice(itemPrices, balance)

        const msg = { name: 'sell', id: id, price: balance }
        parentPort.postMessage(msg);
        bot.needSell = true
        return
    }


    if (messageText.includes('[☃]') && messageText.includes('выставлен на продажу!')) {
        if (bot.typeSell) {
            const msg = { name: 'try-sell', id: bot.typeSell }
            parentPort.postMessage(msg);
        }
        bot.count++
        return
    }
    if (messageText.includes('Не так быстро..')) {
        await delay(getRandomDelayInRange(500, 700));
        if (bot.currentWindow) {
            bot.closeWindow(bot.currentWindow);
        }
        await delay(getRandomDelayInRange(500, 700));
        bot.menu = analysisAH;
        await safeAH(bot);
        return
    }//Данная команда недоступна в режиме AFK
    if (messageText.includes('Данная команда недоступна в режиме AFK')) {
        await delay(getRandomDelayInRange(500, 700));
        if (bot.currentWindow) {
            bot.closeWindow(bot.currentWindow);
        }
        await delay(getRandomDelayInRange(500, 700));
        await walk(bot)
        await delay(getRandomDelayInRange(500, 700));
        bot.menu = analysisAH;
        await safeAH(bot)
        return
    }//[☃] После входа на режим необходимо немного подождать перед использованием аукциона. Подождите
    if (messageText.includes('[☃] После входа на режим необходимо немного подождать перед использованием аукциона. Подождите')) {
        await delay(getRandomDelayInRange(500, 700));
        if (bot.currentWindow) {
            bot.closeWindow(bot.currentWindow);
        }
        await walk(bot)
        await delay(10000);
        bot.menu = analysisAH;
        await safeAH(bot);
        return
    }
    if (messageText.includes('[☃] Не удалось выставить')) {
        bot.ahFull = true;
        return
    }//[✘] Ошибка! У Вас переполнено Хранилище!
    if (messageText.includes('[✘] Ошибка! У Вас переполнено Хранилище!')) {
        bot.ahFull = true;
        return
    }

     if (messageText.includes('[✘] Ошибка! У Вас не хватает Монет!')) {
        await delay(getRandomDelayInRange(500, 700));
        if (bot.currentWindow) {
            bot.closeWindow(bot.currentWindow);
        }
        await delay(getRandomDelayInRange(500, 700));
        bot.chat('/clan withdraw 3000000')
        await delay(getRandomDelayInRange(500, 700));
        bot.menu = analysisAH;
        await safeAH(bot);
    }
    if (messageText.includes('[⚠] Данной команды не существует!')) {
        bot.chat(anarchyCommand)
        await delay(11000)
        await safeAH()
        return
    }

    if (messageText.includes('[$] Ваш баланс:')) {
        let balanceStr = messageText
        if (messageText.includes('.')) {
            balanceStr = balanceStr.slice(0, -3)
        }
        balanceStr = balanceStr.replace(/\D/g, '')
        const balance = parseInt(balanceStr);

        if (isNaN(balance)) {
            logger.error('баланс NAN')
            return
        }
        if (balance - minBalance >= 10000000) {
            await delay(500)
            bot.chat(`/clan invest ${balance - minBalance}`)
        }
        return
    }
    if (messageText.includes('[☃] Максимальная цена')) {
        let balanceStr = messageText;
        
        // Убираем точки (тысячные разделители)
        if (messageText.includes('.')) {
            balanceStr = balanceStr.slice(0, -3);
        }
        
        // Оставляем только цифры
        balanceStr = messageText.replace(/\./g, '').replace(/\D/g, '');
        const balance = parseInt(balanceStr);
        
        // Получаем информацию о предмете
        const slotHotBar = bot.quickBarSlot;
        const slot = transform(slotHotBar);
        const currentPrice = getPriceByEnchantments(bot.inventory.slots[slot], itemPrices);
        const id = getIDByEnchantments(bot.inventory.slots[slot], itemPrices);
        
        // Округляем до десятков тысяч в меньшую сторону
        const basePrice = Math.floor(balance / 10000) * 10000;
        
        // Маркер предмета (последние 2 цифры)
        const marker = currentPrice % 100;
        
        // Финальная цена = базовая + маркер, но не больше баланса
        let finalPrice = basePrice + marker;
        
        // Если получилось больше баланса, отнимаем 100 (переходим на следующий десяток тысяч)
        if (finalPrice > balance) {
            finalPrice = basePrice - 100 + marker; // или basePrice - (100 - marker)
        }
        
        // Отправляем на сервер
        parentPort.postMessage({
            name: "set_max_price", 
            type: id, 
            price: finalPrice
        });
        
        return;
    }

    if (messageText.includes('[☃] Минимальная цена')) {
        let balanceStr = messageText;
        
        // Убираем точки (тысячные разделители)
        if (messageText.includes('.')) {
            balanceStr = balanceStr.slice(0, -3);
        }
        
        // Оставляем только цифры
        balanceStr = messageText.replace(/\./g, '').replace(/\D/g, '');
        const balance = parseInt(balanceStr);

        
        
        // Получаем информацию о предмете
        const slotHotBar = bot.quickBarSlot;
        const slot = transform(slotHotBar);
        const currentPrice = getPriceByEnchantments(bot.inventory.slots[slot], itemPrices);
        const id = getIDByEnchantments(bot.inventory.slots[slot], itemPrices);
        const nacenka = getNacenkaByEnchantments(bot.inventory.slots[slot], itemPrices)
        
        // Округляем до десятков тысяч в большую сторону
        const basePrice = Math.ceil(balance / 10000) * 10000;
        
        // Маркер предмета (последние 2 цифры)
        const marker = currentPrice % 100;
        
        // Финальная цена = базовая + маркер
        let finalPrice = basePrice + marker + nacenka;
        
        // Отправляем на сервер
        parentPort.postMessage({
            name: "set_min_price", 
            type: id, 
            price: finalPrice
        });
        
        return;
    }
})
}

function getIdBySellPrice(itemPrices, val) {
    // Ищем предмет с точным совпадением цены
    const foundItem = itemPrices.find(item => item.priceSell % 100 === val % 100);

    // Если нашли - возвращаем id, иначе null
    return foundItem ? foundItem.id : "";
}

function countTotalItemsInWindow(bot, itemPrices) {
    if (!bot.currentWindow || !bot.currentWindow.slots) {
        return 0;
    }
    
    let totalCount = 0;
    
    for (let slot = 0; slot <= 45; slot++) {
        const slotData = bot.currentWindow.slots[slot];
        if (!slotData) continue;
        
        if (isItemMatchingConfig(slotData, itemPrices)) {
            totalCount++;
        }
    }
    
    return totalCount;
}

async function sellItems(bot, itemPrices) {
    bot.needSell = false;

    if (mu) {
        await delay(500);
        await safeAH(bot);
        return;
    }

    mu = true;

    await walk(bot);
    logger.info(`${bot.username} - прогулка завершена`);


    try {
        while (Date.now() - bot.timeLogin < 13000) {
            await delay(1000);
        }
        bot.timeActive = Date.now();

        if (bot.currentWindow) {
            bot.closeWindow(bot.currentWindow);
            await delay(getRandomDelayInRange(300, 500));
        }

        // Пока аукцион не заполнен
        while (!bot.ahFull) {
            let soldAnything = false;

            // 1. Проверяем горячие слоты (0–8)
            for (let quickSlot = 0; quickSlot < 9; quickSlot++) {
                if (bot.ahFull) break;

                const slotIndex = firstSellSlot + quickSlot;
                const item = bot.inventory.slots[slotIndex];
                if (!item) continue;

                const price = getBestSellPrice(bot, item, itemPrices);
                if (price > 0) {
                    if (bot.quickBarSlot !== quickSlot) {
                        await bot.setQuickBarSlot(quickSlot);
                        await delay(getRandomDelayInRange(400, 600));
                    }
                    bot.chat(`/ah sell ${price}`);
                    await delay(getRandomDelayInRange(100, 200));
                    bot.chat(`/ah sell ${price}`);

                    soldAnything = true;
                    await delay(getRandomDelayInRange(600, 800));
                } else {
                    await bot.tossStack(item);
                    await delay(getRandomDelayInRange(300, 500));
                }
            }

            // 2. Основной инвентарь, если ещё есть место
            if (!bot.ahFull) {
                // Находим свободный слот в горячей панели
                let freeSlot = null;
                for (let i = 0; i < 9; i++) {
                    if (!bot.inventory.slots[i + firstSellSlot]) {
                        freeSlot = i;
                        break;
                    }
                }

                if (freeSlot !== null) {
                    for (let invSlot = 0; invSlot < 27; invSlot++) {
                        if (bot.ahFull) break;

                        const item = bot.inventory.slots[invSlot];
                        if (!item) continue;

                        const price = getBestSellPrice(bot, item, itemPrices);
                        if (price > 0) {
                            await bot.setQuickBarSlot(freeSlot);
                            await delay(300);
                            await bot.moveSlotItem(invSlot, firstSellSlot + freeSlot);
                            await delay(getRandomDelayInRange(500, 700));

                            bot.chat(`/ah sell ${price}`);
                            await delay(getRandomDelayInRange(100, 200));
                            bot.chat(`/ah sell ${price}`);

                            soldAnything = true;
                            await delay(getRandomDelayInRange(600, 800));
                        } else {
                            await bot.tossStack(item);
                            await delay(getRandomDelayInRange(300, 500));
                        }
                    }
                }
            }

            // Условие выхода: ничего не продали за этот проход
            if (!soldAnything) break;
        }
    } catch (error) {
        logger.error(`${bot.username} - Ошибка в sellItems: ${error.stack || error}`);
    } finally {
        logger.info(`${bot.username} - продажа завершена`);
        await delay(500);

        await delay(300)

        for (let i = firstAHSlot; i < lastInventorySlot; i++) {
            const slotData = bot.inventory.slots[i];
            if (!slotData) continue; // пустой слот

            // Проверяем, подходит ли предмет под какую-либо категорию
            const sortedConfig = [...itemPrices].sort((a, b) => b.num - a.num);
            if (!isItemMatchingConfig(slotData, itemPrices)) {
                await bot.tossStack(slotData)
                await delay(300)
            }
        }

        bot.chat('/balance');
        await delay(500);

        await delay(300)
        bot.menu = 'clan'
        bot.chat('/clan storage')
    }
}

function transform(num) {
    if (num < 0 || num > 8) return num; // защита от некорректных значений
    return 44 - (8 - num); // или 36 + num
}

/**
 * Находит лучшую цену продажи для предмета на основе зачарований.
 * @param {Object} item - Предмет (из inventory.slots или window.slots).
 * @param {Array} itemPrices - Конфиг с шаблонами цен.
 * @returns {number} Цена продажи (или 0, если предмет не подходит под конфиг).
 */
function getBestSellPrice(bot, item, itemPrices) {
    return getSellPrice(item, itemPrices);
}

function getID(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.id : 0;
}

function generateRandomKey(bot) {
    bot.key = Math.random().toString(36).substring(2, 15);
}

async function delay(time) {
    return new Promise(resolve => setTimeout(resolve, time));
}

async function safeClick(bot, slot, time) {
    await delay(time);

    if (bot.currentWindow) {
        bot.timeActive = Date.now();
        await bot.clickWindow(slot, leftMouseButton, noShift);
    }
}

async function safeAH(bot) {
    if (mu) return
    netakbistro = true
    let key = bot.key;
    bot.timeActive = Date.now();
    bot.menu = analysisAH
    bot.updateWindow = true
    while (key === bot.key) {
        bot.chat(ahCommand);
        await delay(1000);
    }
}

async function getAHSlotsIDs(bot, itemPrices) {
    if (!bot.currentWindow?.slots) return [];
    const ids = []
    for (let i = 0; i < 8; i++) {
        if (bot.currentWindow?.slots[i]) {
            ids.push(getID(bot.currentWindow?.slots[i]), itemPrices)
        }
    }
    return ids
}

async function getBestAHSlot(bot, itemPrices) {
    if (!bot.currentWindow?.slots) return null;

    for (let slot = firstAHSlot; slot <= 17; slot++) {
        const slotData = bot.currentWindow.slots[slot];
        if (!slotData) continue;
        
        // Получаем UUID текущего предмета
        const currentUUID = getItemUUID(slotData);
        
        // Проверяем, не покупает ли уже кто-то этот лот по UUID
        if (currentUUID && itemsBuying && itemsBuying.length > 0) {
            if (itemsBuying.includes(currentUUID)) {
                console.log(`⏭️ Пропускаем лот ${currentUUID}, уже в очереди на покупку`);
                continue;
            }
        }
        
        // Ищем подходящий конфиг с проверкой прочности
        const config = findMatchingConfigItem(slotData, itemPrices, { 
            checkDurability: true,  // Проверяем прочность
            checkMissingEnchants: true 
        });
        
        if (!config) continue;
        
        try {
            const price = await getBuyPrice(slotData);
            if (!price || price >= config.priceSell - config.nacenka) continue;
            if (!config.priceSell) continue;

            bot.type = config.id;
            if (!bot.type) logger.error('id undefined');
            
            // Отправляем UUID предмета вместо полного объекта
            const message = {name: 'buying', data: currentUUID};
            parentPort.postMessage(message);
            
            return slotData.slot;
        } catch (error) {
            console.error(error);
            continue;
        }
    }
    return null;
}

// Вспомогательная функция для получения UUID
function getItemUUID(item) {
    if (!item || !item.nbt?.value?.PublicBukkitValues?.value?.['auctions:if-uuid']?.value) {
        return null;
    }
    
    try {
        const uuidArray = item.nbt.value.PublicBukkitValues.value['auctions:if-uuid'].value;
        // Преобразуем byteArray в строку для удобства сравнения
        return uuidArray.join(',');
    } catch (e) {
        console.error('Ошибка получения UUID:', e);
        return null;
    }
}

function findFirstMatchingSlotInWindow(bot, itemPrices) {
    if (!bot.currentWindow || !bot.currentWindow.slots) return null;
    
    for (let slot = 0; slot <= 45; slot++) {
        const slotData = bot.currentWindow.slots[slot];
        if (!slotData) continue;
        
        if (isItemMatchingConfig(slotData, itemPrices)) {
            return slot;
        }
    }
    
    return null;
}

function findFirstMatchingSlotInInventory(bot, itemPrices) {
    if (!bot.currentWindow || !bot.currentWindow.slots) {
        return null; // если окна нет, возвращаем null
    }
    
    const sortedConfig = [...itemPrices].sort((a, b) => b.num - a.num);
    
    // Проходим по слотам с 0 по 45
    for (let slot = 63; slot <= 89; slot++) {
        const slotData = bot.currentWindow.slots[slot];
        if (!slotData) continue; // пустой слот
        
        // Проверяем, подходит ли предмет под какую-либо категорию
        if (isItemMatchingConfig(slotData, itemPrices)) {
            return slot;
        }
    }
    
    return null; // ничего не нашли
}

function getPriceByEnchantments(slotData, itemPrices) {
    return getSellPrice(slotData, itemPrices);
}

function getIDByEnchantments(slotData, itemPrices) {
    return getItemId(slotData, itemPrices);
}

function getNacenkaByEnchantments(slotData, itemPrices) {
    return getItemNacenka(slotData, itemPrices);
}

function removeSlotAndTime(obj) {
  // Создаем глубокую копию объекта, чтобы не мутировать оригинал
  const result = JSON.parse(JSON.stringify(obj));
  
  // Удаляем поле slot
  delete result.slot;
  
  try {
    // Получаем массив строк Lore
    const loreEntries = result.nbt.value.display.value.Lore.value.value;
    
    // Находим индекс строки со временем
    const timeIndex = loreEntries.findIndex(entry => 
      entry.includes('Истeкaeт:') || 
      entry.includes('Истекает:') ||
      entry.includes('expires:') ||
      entry.includes('⟲')
    );
    
    // Удаляем строку со временем, если найдена
    if (timeIndex !== -1) {
      loreEntries.splice(timeIndex, 1);
    }
    
  } catch (error) {
    console.warn('Не удалось удалить строку со временем:', error.message);
  }
  
  return result;
}



/**
/**
 * Централизованная функция для поиска подходящего конфига предмета
 * @param {Object} item - Предмет (из inventory.slots или window.slots)
 * @param {Array} itemPrices - Конфиг с шаблонами цен
 * @param {Object} options - Дополнительные опции
 * @param {boolean} options.checkDurability - Проверять ли прочность (по умолчанию true)
 * @param {boolean} options.checkMissingEnchants - Проверять ли отсутствующие зачарования (по умолчанию true)
 * @returns {Object|null} - Найденный конфиг или null
 */
function findMatchingConfigItem(item, itemPrices, options = { checkDurability: true, checkMissingEnchants: true }) {
    if (!item || !itemPrices?.length) return null;

    // Фильтруем конфиг: исключаем те, у которых ID заканчивается на "1.21"
    const filteredConfig = itemPrices.filter(config => !config.id.endsWith('1.21'));
    
    // Если после фильтрации ничего не осталось, возвращаем null
    if (filteredConfig.length === 0) return null;
    
    // Сортируем отфильтрованный конфиг по num
    const sortedConfig = [...filteredConfig].sort((a, b) => b.num - a.num);
    
    // Получаем все зачарования предмета
    const enchantments = item.nbt?.value?.Enchantments?.value?.value || [];
    const customEnchantments = item.nbt?.value?.['custom-enchantments']?.value?.value || [];

    const allEnchants = [
        ...enchantments.map(e => ({ name: e.id?.value, lvl: e.lvl?.value })),
        ...customEnchantments.map(e => ({ name: e.type?.value, lvl: e.level?.value }))
    ];

    for (const configItem of sortedConfig) {
        // Проверка имени
        if (item.name !== configItem.name) continue;

        // Проверка требуемых зачарований
        const areEnchantsValid = configItem.effects?.every(required => {
            const foundEnchant = allEnchants.find(e => e.name === required.name);
            return foundEnchant && foundEnchant.lvl >= required.lvl;
        });

        if (!areEnchantsValid) continue;

        // Проверка на отсутствующие зачарования (нежелательные)
        if (options.checkMissingEnchants) {
            const hasMissingEnchants = allEnchants.some(en => {
                if (!missingEnchantsNames.includes(en.name)) return false;
                const isRequiredByConfig = configItem.effects?.some(ef => ef.name === en.name);
                return !isRequiredByConfig;
            });
            if (hasMissingEnchants) continue;
        }

        // Спецпроверка для кирки
        if (item.name === 'netherite_pickaxe' &&
            allEnchants.some(en => en.name === 'minecraft:silk_touch') &&
            !allEnchants.some(en => en.name === 'melting')
        ) {
            continue;
        }

        // Проверка прочности
        if (options.checkDurability && item.maxDurability) {
            let coefficient = 0.9;
            if (allEnchants.some(en => en.name === 'minecraft:mending')) coefficient = 0.75;
            const damage = item.nbt?.value?.Damage?.value || 0;
            const durabilityLeft = item.maxDurability - damage;
            if (durabilityLeft < item.maxDurability * coefficient) continue;
        }

        return configItem;
    }

    return null;
}

/**
 * Получить цену продажи предмета
 */
function getSellPrice(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.priceSell : 0;
}

/**
 * Получить ID предмета
 */
function getItemId(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.id : "";
}

/**
 * Получить наценку предмета
 */
function getItemNacenka(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.nacenka : 0;
}

/**
 * Получить минимальную цену продажи
 */
function getMinSellPrice(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.minPrice : 0;
}

/**
 * Проверить, подходит ли предмет под конфиг (булево)
 */
function isItemMatchingConfig(item, itemPrices) {
    return findMatchingConfigItem(item, itemPrices) !== null;
}

/**
 * Получить всю информацию о предмете из конфига
 */
function getItemConfig(item, itemPrices) {
    return findMatchingConfigItem(item, itemPrices);
}

async function getBuyPrice(slotData) {
    const loreArray = slotData.nbt?.value?.display?.value?.Lore?.value?.value;
    if (!loreArray) return undefined;

    for (const jsonString of loreArray) {
        try {
            const parsedData = JSON.parse(jsonString);
            
            // Функция для рекурсивного поиска цены
            function findPrice(obj) {
                if (!obj) return null;
                
                // Если это строка и содержит цифры с запятыми
                if (typeof obj === 'string') {
                    const match = obj.match(/[\d,]+/);
                    if (match) return match[0];
                }
                
                // Если это объект с extra массивом
                if (obj.extra && Array.isArray(obj.extra)) {
                    for (const item of obj.extra) {
                        const found = findPrice(item);
                        if (found) return found;
                    }
                }
                
                // Если это объект с text полем
                if (obj.text && typeof obj.text === 'string') {
                    const match = obj.text.match(/[\d,]+/);
                    if (match) return match[0];
                }
                
                return null;
            }
            
            // Ищем цену рекурсивно
            const priceStr = findPrice(parsedData);
            if (priceStr) {
                const price = parseInt(priceStr.replace(/,/g, ''));
                if (!isNaN(price)) return price;
            }
            
        } catch (e) {
            // Игнорируем ошибки парсинга отдельных строк
            continue;
        }
    }

    logger.error('Цена не найдена');
    saveToJsonFile('error.json', slotData);
    return undefined;
}

async function getBuyPriceInStorage(slotData) {
    const loreArray = slotData?.nbt?.value?.display?.value?.Lore?.value?.value;
    if (!Array.isArray(loreArray)) return undefined;

    for (const jsonString of loreArray) {
        try {
            const parsed = JSON.parse(jsonString);

            // Вариант 1: ищем структуру где text = "$" и есть extra с ценой
            if (parsed.text === '$' && parsed.extra?.[0]?.extra?.[0]?.extra?.[0]) {
                const priceStr = parsed.extra[0].extra[0].extra[0];
                if (typeof priceStr === 'string') {
                    const price = parseInt(priceStr.replace(/[^\d]/g, ''));
                    if (!isNaN(price)) return price;
                }
            }

            // Вариант 2: рекурсивный поиск любой строки с цифрами
            function findPriceInExtra(obj) {
                if (!obj) return null;
                
                if (typeof obj === 'string') {
                    const match = obj.match(/[\d,]+/);
                    return match ? match[0] : null;
                }
                
                if (Array.isArray(obj)) {
                    for (const item of obj) {
                        const found = findPriceInExtra(item);
                        if (found) return found;
                    }
                }
                
                if (obj.extra && Array.isArray(obj.extra)) {
                    for (const item of obj.extra) {
                        const found = findPriceInExtra(item);
                        if (found) return found;
                    }
                }
                
                if (obj.text && typeof obj.text === 'string') {
                    const match = obj.text.match(/[\d,]+/);
                    if (match) return match[0];
                }
                
                return null;
            }

            // Пробуем найти цену рекурсивно
            const priceStr = findPriceInExtra(parsed);
            if (priceStr) {
                const price = parseInt(priceStr.replace(/[^\d]/g, ''));
                if (!isNaN(price)) return price;
            }

        } catch (e) {
            // Игнорируем строки, которые не парсятся
            continue;
        }
    }

    // Если цена не найдена — логируем и сохраняем
    console.error('Цена не найдена');
    // saveToJsonFile('error.json', slotData); // раскомментируй если нужно

    return undefined;
}

function getRandomDelayInRange(min, max) {
    return Math.floor(Math.random() * (max - min + 1)) + min;
}

if (workerData) {
    launchBookBuyer(workerData.username, workerData.password, workerData.anarchy);
}

function getRandomElement(array) {
    if (!Array.isArray(array) || array.length === 0) {
        throw new Error("Input must be a non-empty array");
    }

    const randomIndex = Math.floor(Math.random() * array.length);
    return array[randomIndex];
}

async function longWalk(bot) {
    await delay(500)
    let timeTP = Date.now()
    bot.autoEat.enableAuto()
    bot.timeActive = Date.now();
    logger.info(`${bot.username} - все забито. Гуляем.`);
    while (bot.ahFull) {  // Гуляем пока ahFull === true
        const resetime = Math.floor((Date.now() - bot.timeReset) / 1000)
        if (resetime > 60 || needReset) {
            await delay(500);
            ['forward', 'back', 'left', 'right'].forEach(move =>
                bot.setControlState(move, false)
            );
            await delay(500);
            await safeAH(bot);
            bot.autoEat.disableAuto()

            return
        }

        // Случайное движение
        const movements = ['forward', 'back', 'left', 'right'];
        const randomMove = movements[Math.floor(Math.random() * movements.length)];
        bot.setControlState(randomMove, true);
        await delay(500);
        bot.setControlState(randomMove, false);
        if (Date.now() - timeTP > 10000) {
            await delay(500)
            timeTP = Date.now()
            const warps = ['mine', 'casino', 'case', 'shop']
            const warp = getRandomElement(warps)
            bot.chat(`/warp ${warp}`)
            await delay(8000)
        }

        await delay(500);
    }

    logger.info(`${bot.username} - опять работать.`);

    // Останавливаем все движения когда ahFull стал false
    ['forward', 'back', 'left', 'right'].forEach(move =>
        bot.setControlState(move, false)
    );

    bot.autoEat.disableAuto()
}

async function walk(bot) {
    await delay(500)
    bot.autoEat.enableAuto()
    const endTime = Date.now() + 4000;

    while (Date.now() < endTime) {

        // Случайное движение
        const movements = ['forward', 'back', 'left', 'right'];
        const randomMove = movements[Math.floor(Math.random() * movements.length)];
        bot.setControlState(randomMove, true);
        await delay(500);
        bot.setControlState(randomMove, false);


        await delay(500);
    }

    // Останавливаем все движения
    ['forward', 'back', 'left', 'right'].forEach(move =>
        bot.setControlState(move, false)
    );

    const warps = ['mine', 'casino', 'case', 'shop']
    const warp = getRandomElement(warps)
    bot.chat(`/warp ${warp}`)
    await delay(8000)

    bot.autoEat.disableAuto()

}

async function safeClickBuy(bot, slot, time, key) {
    let timeDelay = time
    if (bot.updateWindow) {
        bot.updateWindow = false
        bot.startClickTime = Date.now()
    } else {
        timeDelay = time - (Date.now() - bot.startClickTime)
        if (timeDelay <= 0) timeDelay = 0
    }
            
    await delay(timeDelay);
    if (bot.key != key) {
        console.log('твари ах обновили и теперь так')
        return
    }
    if (slot === 52) bot.timeReset = Date.now();
    bot.updateWindow = true
    if (bot.currentWindow) {
        bot.timeActive = Date.now();
        await bot.clickWindow(slot, leftMouseButton, 1);
    }
}

function normalizeItemData(obj) {
    if (!obj) return null;

    // 1. Создаем глубокую копию
    const result = JSON.parse(JSON.stringify(obj));

    // 2. Удаляем поле slot, так как оно меняется при перелистывании страниц
    delete result.slot;

    try {
        const loreEntries = result.nbt.value.display.value.Lore.value.value;

        // 3. Вычисляем оставшееся время в секундах
        const secondsLeft = extractTimeToSeconds(result);

        // 4. Находим индекс строки со временем, чтобы заменить её
        const timeIndex = loreEntries.findIndex(entry =>
            entry.includes('Истeкaeт:') ||
            entry.includes('Истекает:') ||
            entry.includes('expires:') ||
            entry.includes('⟲')
        );

        if (timeIndex !== -1) {
            if (secondsLeft !== null) {
                // Вычисляем Unix Timestamp окончания (в миллисекундах)
                const expirationTimestamp = Date.now() + (secondsLeft * 1000);
                
                // Заменяем строку Lore на метку времени. 
                // Мы сохраняем формат строки, чтобы JSON.stringify не ломался.
                loreEntries[timeIndex] = `{"text":"EXP_TS:${expirationTimestamp}"}`;
            }
        }

    } catch (error) {
        console.warn('Ошибка при нормализации времени:', error.message);
    }

    return result;
}

function extractTimeToSeconds(nbtData) {
    try {
        const loreList = nbtData?.nbt?.value?.display?.value?.Lore?.value?.value;
        if (!loreList) throw new Error('Lore не найден');

        let timeLine = "";

        // 1. Извлекаем чистый текст из JSON Lore
        for (const rawEntry of loreList) {
            try {
                const parsed = JSON.parse(rawEntry);
                let fullText = parsed.text || "";
                if (parsed.extra) fullText += parsed.extra.map(e => e.text).join("");
                
                // Проверяем на "Истекает" (учитываем возможную латиницу в буквах 'е' или 'а')
                if (/Ист.к.ет:/i.test(fullText)) {
                    timeLine = fullText;
                    break;
                }
            } catch (e) {
                if (/Ист.к.ет:/i.test(rawEntry)) {
                    timeLine = rawEntry;
                    break;
                }
            }
        }

        if (!timeLine) return null;

        // 2. Ищем каждое значение отдельно (флаг 'i' для любого регистра)
        // \d+ — одна или более цифр
        // \s* — возможные пробелы
        const hMatch = timeLine.match(/(\d+)\s*ч/i);
        const mMatch = timeLine.match(/(\d+)\s*мин/i);
        const sMatch = timeLine.match(/(\d+)\s*сек/i);

        // 3. Конвертируем в числа (если не найдено — берем 0)
        const hours   = hMatch ? parseInt(hMatch[1], 10) : 0;
        const minutes = mMatch ? parseInt(mMatch[1], 10) : 0;
        const seconds = sMatch ? parseInt(sMatch[1], 10) : 0;

        const totalSeconds = (hours * 3600) + (minutes * 60) + seconds;

        // Если распарсили 0 (и это не странно), либо если вообще цифр не было
        if (totalSeconds === 0 && !timeLine.includes('0')) {
             throw new Error('Цифры времени не обнаружены в строке: ' + timeLine);
        }

        return totalSeconds;

    } catch (error) {
        console.error('Ошибка парсинга:', error.message);
        return null;
    }
}

async function saveToJsonFile(filePath, data) {
    const tempPath = `${filePath}.tmp`; // Временный файл
    try {
        const jsonString = JSON.stringify(data, null, 2);
        
        // 1. Пишем во временный файл
        await writeFile(tempPath, jsonString, 'utf8');
        
        // 2. Атомарно заменяем старый файл новым
        await rename(tempPath, filePath);
        
        console.log('✅ Данные успешно сохранены:', filePath);
    } catch (error) {
        console.error('❌ Ошибка при сохранении:', error);
        // Пытаемся почистить временный файл, если он остался
        try { await unlink(tempPath); } catch {}
    }
}