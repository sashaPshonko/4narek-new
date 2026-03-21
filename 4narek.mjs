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
let mu = false
let isKrush = false
let needSendAH = true

// Глобальные переменные для состояния бота
let botStartTime = Date.now() - 55000
let botAhFull = false
let botTimeReset = Date.now()
let botLogin = true
let botTimeActive = Date.now()
let botTimeLogin = Date.now()
let botPrices = []
let botCount = 0
let botAh = []
let botNeedSell = false
let botStartClickTime = null
let botUpdateWindow = false
let botMenu = 'Выбор скупки ресурсов'
let botKey = null
let botType = ""
let botTypeSell = null
let enoughItems = false
let lastWarpTP = Date.now() - 40000

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
    let bot = {}
    await delay(getRandomDelayInRange(0, 10000))
    
    bot = mineflayer.createBot({
        host: 'mc.funtime.su',
        port: 25565,
        username: name,
        password: password,
        version: '1.16.5',
        chatLengthLimit: 256,
        viewDistance: 'tiny'
    });

    const loginCommand = `/l ${name}`;
    const anarchyCommand = `/an${anarchy}`;
    const shopCommand = '/shop';

    console.warn = () => { };

    bot.once('login', async () => {
        bot.loadPlugin(autoEat)
        botStartTime = Date.now() - 55000;
        botAhFull = false;
        botTimeReset = Date.now()
        botLogin = true;
        botTimeActive = Date.now();
        botTimeLogin = Date.now()
        botPrices = []
        botCount = 0
        netakbistro = true
        botAh = []
        botNeedSell = false
        botStartClickTime = null
        botUpdateWindow = false

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
        if (Date.now() - botTimeActive > 90000) {
            mu = false
            botTimeActive = Date.now();
            botMenu = analysisAH
            await safeAH(bot);
        }
    });

    bot.on('windowOpen', async () => {
        let key = ""
        switch (botMenu) {
            case chooseBuying:
                const msg = { name: 'success', username: workerData.username };
                parentPort.postMessage(msg);
                await delay(3000);
                logger.info(`${name} - ${botMenu}`);
                botMenu = setSectionFarmer;

                await safeClick(bot, slotToChooseBuying, minDelay);
                break;

            case setSectionFarmer:
                logger.info(`${name} - ${botMenu}`);
                botMenu = sectionFarmer;

                await safeClick(bot, slotToSetSectionFarmer, minDelay);
                break;

            case sectionFarmer:
                logger.info(`${name} - ${botMenu}`);
                botMenu = setSectionFood;

                await safeClick(bot, slotToLeaveSection, minDelay);
                break;

            case setSectionFood:
                logger.info(`${name} - ${botMenu}`);
                botMenu = sectionFood;

                await safeClick(bot, slotToSetSectionFood, minDelay);
                break;

            case sectionFood:
                logger.info(`${name} - ${botMenu}`);
                botMenu = setSectionResources;

                await safeClick(bot, slotToLeaveSection, minDelay);
                break;

            case setSectionResources:
                logger.info(`${name} - ${botMenu}`);
                botMenu = sectionResources;

                await delay(getRandomDelayInRange(1000, 2500));
                await safeClick(bot, slotToSetSectionResources, minDelay);
                break;

            case sectionResources:
                logger.info(`${name} - ${botMenu}`);
                botMenu = setSectionLoot;

                await delay(getRandomDelayInRange(1000, 2500));
                await safeClick(bot, slotToLeaveSection, minDelay);
                break;

            case setSectionLoot:
                logger.info(`${name} - ${botMenu}`);
                botMenu = sectionLoot;

                await delay(getRandomDelayInRange(1000, 2500));
                await safeClick(bot, slotToSetSectionLoot, minDelay);
                break;

            case sectionLoot:
                logger.info(`${name} - ${botMenu}`);
                botMenu = analysisAH;
                await delay(5000);
                bot.closeWindow(bot.currentWindow);
                await delay(500);

                while (Date.now() - botTimeLogin < 13000) {
                    await delay(1000)
                }
                await safeAH(bot);
                break;

            case analysisAH:
                logger.info(`${name} - ${botMenu}`);
                botTimeActive = Date.now();
                generateRandomKey(bot);
                key = botKey
                const uptime = Math.floor((Date.now() - botStartTime) / 1000);
                if (uptime > 55 || botNeedSell) {
                    logger.info(`${name} - продажа`);
                    await sellItems(bot, itemPrices)
                    break;
                }

                
                const resetime = Math.floor((Date.now() - botTimeReset) / 1000)
                if (resetime > 60 || needReset || enoughItems) {
                    needSendAH = true
                    logger.info(`${name} - ресет`);
                    await delay(500);
                    botMenu = myItems;
                    await safeClickBuy(bot, 46, getRandomDelayInRange(700, 1300), key)
                    break;
                }
                
                logger.info(`${name} - ${botMenu}`);
                let count = 0
                for (let i = firstInventorySlot; i <= lastInventorySlot; i++) {
                    if (bot.inventory.slots[i]) count++
                }
                        
                if (count >= 36 - botCount) {
                    logger.error('Инвентарь заполнен')
                    await sellItems(bot, itemPrices)
                    break;
                }

                if (bot.currentWindow.slots[0].name?.includes('stained_glass')) {
                    await safeClickBuy(bot, 0, getRandomDelayInRange(150-300), key)
                    break
                }
                
                logger.info(`${name} - поиск лучшего предмета`);
                let slotToBuy = await getBestAHSlot(bot, itemPrices);

                switch (slotToBuy) {
                    case null:
                        botMenu = analysisAH;
                        await safeClickBuy(bot, slotToReloadAH, getRandomDelayInRange(1500, 4500), key);
                        break;
                    default:
                        if (netakbistro) {
                            netakbistro = false;
                            await safeClickBuy(bot, slotToBuy, 1655, key);
                        } else if (slotToBuy < 9) {
                            await safeClickBuy(bot, slotToBuy, getRandomDelayInRange(100, 150) * (slotToBuy + 1), key);
                        } else {
                            await safeClickBuy(bot, slotToReloadAH, getRandomDelayInRange(1500, 4500), key);
                        }
                        break;
                }
                break;

            case myItems:
                generateRandomKey(bot);
                key = botKey;
                if (bot.currentWindow.slots[27]) {
                    logger.error('суки обновили аукцион');
                    break;
                }

                if (needSendAH) {
                    for (let i = 0; i < 8; i++) {
                        const currentSlot = bot.currentWindow?.slots[i];
                        if (currentSlot) {
                            botCount++;
                            const id = getIDByEnchantments(currentSlot, itemPrices);
                            botAh.push(id);
                        } else break;
                    }

                    parentPort.postMessage({ name: 'items', username: bot.username, items: botAh });
                    needSendAH = false

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
                    }

                await delay(500);
                needReset = false;
                logger.info(`${name} - ${botMenu}`);
                
                botCount = 0;
                botAh = [];
                let slot = null;

                for (let i = 0; i < 8; i++) {
                    const currentSlot = bot.currentWindow?.slots[i];
                    if (!currentSlot) break;

                    const priceOnAH = getBuyPriceInStorage(currentSlot);
                    const priceSell = await getPriceByEnchantments(currentSlot, itemPrices);

                    if (priceSell !== priceOnAH || enoughItems) {
                        logger.error(`chnge ${priceSell} ${priceOnAH}`);
                        botAhFull = false;
                        slot = i;
                        break;
                    }
                }

                if (slot !== null) {
                    botAhFull = false;
                    botNeedSell = true;
                    botMenu = myItems;
                    await safeClickBuy(bot, slot, getRandomDelayInRange(700, 1300), key);
                    break;
                }

                if (Math.floor((Date.now() - botTimeReset) / 1000) > 60) {
                    botMenu = setAH;
                    await safeClickBuy(bot, 52, getRandomDelayInRange(700, 1300), key);
                } else {
                    botMenu = analysisAH;
                    await safeClickBuy(bot, 46, getRandomDelayInRange(700, 1300), key);
                }
                break;
         
            case setAH:
                generateRandomKey(bot)
                key = botKey
                logger.info(`${name} - ${botMenu}`);
                botMenu = analysisAH;

                await safeClickBuy(bot, 46, getRandomDelayInRange(700, 1300), key)
                break;

            case "clan":
                logger.info(`${bot.username} ${botMenu}`)
                generateRandomKey(bot)
        
                let countItems = countTotalItemsInWindow(bot, itemPrices)
                if (botAhFull && countItems === 0) {
                    const slot = findFirstMatchingSlotInInventory(bot, itemPrices)
                    if (slot) {
                        logger.info(`${bot.username} добавил`)
                        await safeClickBuy(bot, slot, 500, botKey)
                    }
                } else if (!botAhFull && countItems > 0) {
                    const slot = findFirstMatchingSlotInWindow(bot, itemPrices)
                    if (slot) {
                        logger.info(`${bot.username} забрал`)
                        botNeedSell = true
                        await safeClickBuy(bot, slot, 500, botKey)
                    }
                }
                logger.info(`${bot.username} никуда не кликнул`)
                await delay(300)
                if (bot.currentWindow) {
                    bot.closeWindow(bot.currentWindow);
                }
                botStartTime = Date.now();
                mu = false;
                logger.info(`${bot.username} - мьютекс снят`);

                await delay(500);
                botMenu = analysisAH;
                await safeAH(bot);
                break
        }
    });

    bot.on('message', async (message) => {
        const messageText = message.toString();
        console.log(messageText)

        if (messageText.includes('[☃] Вы успешно купили')) {
            botNeedSell = true
            let balanceStr = messageText
            if (messageText.includes('.')) {
                balanceStr = balanceStr.slice(0, -3)
            }
            balanceStr = balanceStr.replace(/\D/g, '')
            const balance = parseInt(balanceStr);
            const msg = { name: 'buy', id: botType, price: balance }
            parentPort.postMessage(msg);
            return
        }

        if (messageText.includes('BotFilter >> Введите номер с картинки в чат')) {
            parentPort.postMessage(`${workerData.username} - ввести капчу`)
            return
        }

        if (messageText.toLowerCase().includes('вы забанены')) {
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
            enoughItems = false 
            botStartTime = Date.now() - 240000;
            botAhFull = false;
            botTimeReset = Date.now() - 60000;
            botLogin = true;
            botTimeActive = Date.now();
            botTimeLogin = Date.now()
            botPrices = []
            botCount = 0
            netakbistro = true

            await delay(minDelay);
            bot.chat(anarchyCommand);
        }

        if (messageText.includes('[☃] У Вас купили')) {
            botAhFull = false;
            enoughItems = false
            let balanceStr = messageText
            if (messageText.includes('.')) {
                balanceStr = balanceStr.slice(0, -3)
            }
            balanceStr = balanceStr.replace(/\D/g, '')
            const balance = parseInt(balanceStr);
            const id = getIdBySellPrice(itemPrices, balance)

            const msg = { name: 'sell', id: id, price: balance }
            parentPort.postMessage(msg);
            botNeedSell = true
            return
        }

        if (messageText.includes('[☃]') && messageText.includes('выставлен на продажу!')) {
            if (botTypeSell) {
                const msg = { name: 'try-sell', id: botTypeSell }
                parentPort.postMessage(msg);
            }
            botCount++
            return
        }
        
        if (messageText.includes('Не так быстро..')) {
            await delay(getRandomDelayInRange(500, 700));
            if (bot.currentWindow) {
                bot.closeWindow(bot.currentWindow);
            }
            await delay(getRandomDelayInRange(500, 700));
            botMenu = analysisAH;
            await safeAH(bot);
            return
        }
        
        if (messageText.includes('Данная команда недоступна в режиме AFK')) {
            await delay(getRandomDelayInRange(500, 700));
            if (bot.currentWindow) {
                bot.closeWindow(bot.currentWindow);
            }
            await delay(getRandomDelayInRange(500, 700));
            await walk(bot)
            await delay(getRandomDelayInRange(500, 700));
            botMenu = analysisAH;
            await safeAH(bot)
            return
        }
        
        if (messageText.includes('[☃] После входа на режим необходимо немного подождать перед использованием аукциона. Подождите')) {
            await delay(getRandomDelayInRange(500, 700));
            if (bot.currentWindow) {
                bot.closeWindow(bot.currentWindow);
            }
            await walk(bot)
            await delay(10000);
            botMenu = analysisAH;
            await safeAH(bot);
            return
        }
        
        if (messageText.includes('[☃] Не удалось выставить') ||
            messageText.includes('[✘] Ошибка! У Вас переполнено Хранилище!')) {
            enoughItems = true
            botAhFull = true;
            return;
        }

        if (messageText.includes('[✘] Ошибка! У Вас не хватает Монет!')) {
            await delay(getRandomDelayInRange(500, 700));
            if (bot.currentWindow) {
                bot.closeWindow(bot.currentWindow);
            }
            await delay(getRandomDelayInRange(500, 700));
            bot.chat('/clan withdraw 3000000')
            await delay(getRandomDelayInRange(500, 700));
            botMenu = analysisAH;
            await safeAH(bot);
        }
        
        if (messageText.includes('[⚠] Данной команды не существует!')) {
            bot.chat(anarchyCommand)
            await delay(11000)
            await safeAH(bot)
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
            
            if (messageText.includes('.')) {
                balanceStr = balanceStr.slice(0, -3);
            }
            
            balanceStr = messageText.replace(/\./g, '').replace(/\D/g, '');
            const balance = parseInt(balanceStr);
            
            const slotHotBar = bot.quickBarSlot;
            const slot = transform(slotHotBar);
            const currentPrice = getPriceByEnchantments(bot.inventory.slots[slot], itemPrices);
            const id = getIDByEnchantments(bot.inventory.slots[slot], itemPrices);
            
            const basePrice = Math.floor(balance / 10000) * 10000;
            const marker = currentPrice % 100;
            
            let finalPrice = basePrice + marker;
            
            if (finalPrice > balance) {
                finalPrice = basePrice - 100 + marker;
            }
            
            parentPort.postMessage({
                name: "set_max_price", 
                type: id, 
                price: finalPrice
            });
            
            return;
        }

        if (messageText.includes('[☃] Минимальная цена')) {
            let balanceStr = messageText;
            
            if (messageText.includes('.')) {
                balanceStr = balanceStr.slice(0, -3);
            }
            
            balanceStr = messageText.replace(/\./g, '').replace(/\D/g, '');
            const balance = parseInt(balanceStr);
            
            const slotHotBar = bot.quickBarSlot;
            const slot = transform(slotHotBar);
            const currentPrice = getPriceByEnchantments(bot.inventory.slots[slot], itemPrices);
            const id = getIDByEnchantments(bot.inventory.slots[slot], itemPrices);
            const nacenka = getNacenkaByEnchantments(bot.inventory.slots[slot], itemPrices)
            
            const basePrice = Math.ceil(balance / 10000) * 10000;
            const marker = currentPrice % 100;
            
            let finalPrice = basePrice + marker + nacenka;
            if (JSON.stringify(bot.inventory.slots[slot]).includes('krush')) {
                isKrush = true
                bot.chat(`ah sell ${finalPrice}`)
                await delay(100)
                bot.chat(`ah sell ${finalPrice}`)
                isKrush = false
                return
            }
            
            parentPort.postMessage({
                name: "set_min_price", 
                type: id, 
                price: finalPrice
            });
            
            return;
        }
    });
}

function getIdBySellPrice(itemPrices, val) {
    const foundItem = itemPrices.find(item => item.priceSell % 100 === val % 100);
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
    botNeedSell = false;

    if (mu) {
        await delay(500);
        await safeAH(bot);
        return;
    }

    mu = true;

    await walk(bot);
    logger.info(`${bot.username} - прогулка завершена`);

    try {
        while (Date.now() - botTimeLogin < 13000) {
            await delay(1000);
        }
        botTimeActive = Date.now();

        if (bot.currentWindow) {
            bot.closeWindow(bot.currentWindow);
            await delay(getRandomDelayInRange(300, 500));
        }

        while (!botAhFull) {
            let soldAnything = false;

            for (let quickSlot = 0; quickSlot < 9; quickSlot++) {
                while (isKrush) await delay(100)
                if (botAhFull) break;

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

            if (!botAhFull) {
                let freeSlot = null;
                for (let i = 0; i < 9; i++) {
                    if (!bot.inventory.slots[i + firstSellSlot]) {
                        freeSlot = i;
                        break;
                    }
                }

                if (freeSlot !== null) {
                    for (let invSlot = 0; invSlot < 27; invSlot++) {
                        while (isKrush) await delay(100)
                        if (botAhFull) break;

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
            if (!slotData) continue;

            if (!isItemMatchingConfig(slotData, itemPrices)) {
                await bot.tossStack(slotData)
                await delay(300)
            }
        }

        bot.chat('/balance');
        await delay(500);

        await delay(300)
        botMenu = 'clan'
        bot.chat('/clan storage')
    }
}

function transform(num) {
    if (num < 0 || num > 8) return num;
    return 44 - (8 - num);
}

function getBestSellPrice(bot, item, itemPrices) {
    return getSellPrice(item, itemPrices);
}

function getID(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.id : 0;
}

function generateRandomKey(bot) {
    botKey = Math.random().toString(36).substring(2, 15);
}

async function delay(time) {
    return new Promise(resolve => setTimeout(resolve, time));
}

async function safeClick(bot, slot, time) {
    await delay(time);

    if (bot.currentWindow) {
        botTimeActive = Date.now();
        await bot.clickWindow(slot, leftMouseButton, noShift);
    }
}

async function safeAH(bot) {
    if (mu) return
    netakbistro = true
    let key = botKey;
    botTimeActive = Date.now();
    botMenu = analysisAH
    botUpdateWindow = true
    while (key === botKey) {
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
        
        const currentUUID = getItemUUID(slotData);
        
        if (currentUUID && itemsBuying && itemsBuying.length > 0) {
            if (itemsBuying.includes(currentUUID)) {
                console.log(`⏭️ Пропускаем лот ${currentUUID}, уже в очереди на покупку`);
                continue;
            }
        }
        
        const config = findMatchingConfigItem(slotData, itemPrices, { 
            checkDurability: true,
            checkMissingEnchants: true 
        });
        
        if (!config) continue;
        
        try {
            const price = await getBuyPrice(slotData);
            if (!price || price >= config.priceSell - config.nacenka) continue;
            if (!config.priceSell) continue;

            botType = config.id;
            if (!botType) logger.error('id undefined');
            
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

function getItemUUID(item) {
    if (!item || !item.nbt?.value?.PublicBukkitValues?.value?.['auctions:if-uuid']?.value) {
        return null;
    }
    
    try {
        const uuidArray = item.nbt.value.PublicBukkitValues.value['auctions:if-uuid'].value;
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
        return null;
    }
    
    for (let slot = 63; slot <= 89; slot++) {
        const slotData = bot.currentWindow.slots[slot];
        if (!slotData) continue;
        
        if (isItemMatchingConfig(slotData, itemPrices)) {
            return slot;
        }
    }
    
    return null;
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


function findMatchingConfigItem(item, itemPrices, options = { checkDurability: true, checkMissingEnchants: true }) {
    if (!item || !itemPrices?.length) return null;

    const filteredConfig = itemPrices.filter(config => !config.id.endsWith('1.21'));
    
    if (filteredConfig.length === 0) return null;
    
    const sortedConfig = [...filteredConfig].sort((a, b) => b.num - a.num);
    
    const enchantments = item.nbt?.value?.Enchantments?.value?.value || [];
    const customEnchantments = item.nbt?.value?.['custom-enchantments']?.value?.value || [];

    const allEnchants = [
        ...enchantments.map(e => ({ name: e.id?.value, lvl: e.lvl?.value })),
        ...customEnchantments.map(e => ({ name: e.type?.value, lvl: e.level?.value }))
    ];

    for (const configItem of sortedConfig) {
        if (item.name !== configItem.name) continue;

        const areEnchantsValid = configItem.effects?.every(required => {
            const foundEnchant = allEnchants.find(e => e.name === required.name);
            return foundEnchant && foundEnchant.lvl >= required.lvl;
        });

        if (!areEnchantsValid) continue;

        if (options.checkMissingEnchants) {
            const hasMissingEnchants = allEnchants.some(en => {
                if (!missingEnchantsNames.includes(en.name)) return false;
                const isRequiredByConfig = configItem.effects?.some(ef => ef.name === en.name);
                return !isRequiredByConfig;
            });
            if (hasMissingEnchants) continue;
        }

        if (item.name === 'netherite_pickaxe' &&
            allEnchants.some(en => en.name === 'minecraft:silk_touch') &&
            !allEnchants.some(en => en.name === 'smelting')
        ) {
            continue;
        }

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

function getSellPrice(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.priceSell : 0;
}

function getItemId(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.id : "";
}

function getItemNacenka(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.nacenka : 0;
}

function getMinSellPrice(item, itemPrices) {
    const config = findMatchingConfigItem(item, itemPrices);
    return config ? config.minPrice : 0;
}

function isItemMatchingConfig(item, itemPrices) {
    return findMatchingConfigItem(item, itemPrices) !== null;
}

function getItemConfig(item, itemPrices) {
    return findMatchingConfigItem(item, itemPrices);
}

async function getBuyPrice(slotData) {
    const loreArray = slotData.nbt?.value?.display?.value?.Lore?.value?.value;
    if (!loreArray) {
        parentPort.postMessage(`нет лора для предмета ${slotData.name}: ${JSON.stringify(bot.currentWindow)}`);
        return undefined;
    }

    for (const jsonString of loreArray) {
        try {
            const parsed = JSON.parse(jsonString);
            
            if (parsed.text === '$' && Array.isArray(parsed.extra)) {
                if (parsed.extra[0]?.extra?.[0]?.extra?.[0]) {
                    const priceStr = parsed.extra[0].extra[0].extra[0];
                    if (typeof priceStr === 'string') {
                        const price = parseInt(priceStr.replace(/,/g, '').replace(/\s/g, ''));
                        if (!isNaN(price)) {
                            if (price > 10000) {
                                return price; // нормальная цена
                            } else {
                                // подозрительно низкая цена
                                parentPort.postMessage(
                                    `подозрительная цена ${price} для ${slotData.name}: ${JSON.stringify(slotData)}`
                                );
                                return undefined;
                            }
                        }
                    }
                }
                // структура с $ есть, но не удалось извлечь число
                parentPort.postMessage(
                    `невозможно извлечь цену из структуры с $ для ${slotData.name}: ${JSON.stringify(slotData)}`
                );
                return undefined;
            }
        } catch (e) {
            continue;
        }
    }

    // объект с $ не найден
    parentPort.postMessage(
        `не найден объект с $ для ${slotData.name}: ${JSON.stringify(slotData)}`
    );
    return undefined;
}

function getBuyPriceInStorage(slotData) {
    const loreArray = slotData?.nbt?.value?.display?.value?.Lore?.value?.value;
    if (!Array.isArray(loreArray)) return undefined;

    for (const jsonString of loreArray) {
        try {
            const parsed = JSON.parse(jsonString);

            if (parsed.text === '$' && parsed.extra?.[0]?.extra?.[0]?.extra?.[0]) {
                const priceStr = parsed.extra[0].extra[0].extra[0];
                if (typeof priceStr === 'string') {
                    const price = parseInt(priceStr.replace(/[^\d]/g, ''));
                    if (!isNaN(price)) return price;
                }
            }

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

            const priceStr = findPriceInExtra(parsed);
            if (priceStr) {
                const price = parseInt(priceStr.replace(/[^\d]/g, ''));
                if (!isNaN(price)) return price;
            }

        } catch (e) {
            continue;
        }
    }

    console.error('Цена не найдена');
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

async function walk(bot) {
    await delay(500)
    bot.autoEat.enableAuto()
    const endTime = Date.now() + 4000;

    while (Date.now() < endTime) {
        const movements = ['forward', 'back', 'left', 'right'];
        const randomMove = movements[Math.floor(Math.random() * movements.length)];
        bot.setControlState(randomMove, true);
        await delay(500);
        bot.setControlState(randomMove, false);

        await delay(500);
    }

    ['forward', 'back', 'left', 'right'].forEach(move =>
        bot.setControlState(move, false)
    );

    const warps = ['mine', 'casino', 'case', 'shop']
    if (Date.now() - lastWarpTP > 40000) {
        const warp = getRandomElement(['mine', 'casino', 'case', 'shop']);
        bot.chat(`/warp ${warp}`);
        await delay(8000);
    }

    bot.autoEat.disableAuto()
}

async function safeClickBuy(bot, slot, time, key) {
    let timeDelay = time
    if (botUpdateWindow) {
        botUpdateWindow = false
        botStartClickTime = Date.now()
    } else {
        timeDelay = time - (Date.now() - botStartClickTime)
        if (timeDelay <= 0) timeDelay = 0
    }
            
    await delay(timeDelay);
    if (botKey != key) {
        console.log('твари ах обновили и теперь так')
        return
    }
    if (slot === 52) botTimeReset = Date.now();
    botUpdateWindow = true
    if (bot.currentWindow) {
        botTimeActive = Date.now();
        await bot.clickWindow(slot, leftMouseButton, 1);
    }
}

function normalizeItemData(obj) {
    if (!obj) return null;

    const result = JSON.parse(JSON.stringify(obj));
    delete result.slot;

    try {
        const loreEntries = result.nbt.value.display.value.Lore.value.value;
        const secondsLeft = extractTimeToSeconds(result);

        const timeIndex = loreEntries.findIndex(entry =>
            entry.includes('Истeкaeт:') ||
            entry.includes('Истекает:') ||
            entry.includes('expires:') ||
            entry.includes('⟲')
        );

        if (timeIndex !== -1) {
            if (secondsLeft !== null) {
                const expirationTimestamp = Date.now() + (secondsLeft * 1000);
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

        for (const rawEntry of loreList) {
            try {
                const parsed = JSON.parse(rawEntry);
                let fullText = parsed.text || "";
                if (parsed.extra) fullText += parsed.extra.map(e => e.text).join("");
                
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

        const hMatch = timeLine.match(/(\d+)\s*ч/i);
        const mMatch = timeLine.match(/(\d+)\s*мин/i);
        const sMatch = timeLine.match(/(\d+)\s*сек/i);

        const hours = hMatch ? parseInt(hMatch[1], 10) : 0;
        const minutes = mMatch ? parseInt(mMatch[1], 10) : 0;
        const seconds = sMatch ? parseInt(sMatch[1], 10) : 0;

        const totalSeconds = (hours * 3600) + (minutes * 60) + seconds;

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
    const tempPath = `${filePath}.tmp`;
    try {
        const jsonString = JSON.stringify(data, null, 2);
        await writeFile(tempPath, jsonString, 'utf8');
        await rename(tempPath, filePath);
        console.log('✅ Данные успешно сохранены:', filePath);
    } catch (error) {
        console.error('❌ Ошибка при сохранении:', error);
        try { await fs.unlink(tempPath); } catch {}
    }
}