# L2 Game Updater

Система обновления файлов для клиента игры L2, написанная на Go. Проект включает серверную и клиентскую части, обеспечивающие безопасное и эффективное обновление игровых файлов.

## Структура проекта

- **client-standalone** - GUI-клиент для обновления файлов с интерфейсом на Fyne
- **server** - Серверная часть для генерации и обслуживания манифеста обновлений
- **standalone** - Служебные файлы для сборки и упаковки клиента

## Основные функции

### Безопасность
- Обфускация имен файлов для хранения в облаке
- AES-256 шифрование архивированных файлов
- Проверка целостности файлов с использованием SHA-256 хешей
- Безопасная передача манифеста обновлений

### Производительность
- Быстрая проверка файлов с использованием размера вместо вычисления полного хеша
- Параллельная проверка файлов (5 одновременных проверок)
- Параллельная загрузка и распаковка архивов
- Прогрессивные индикаторы для отслеживания процесса обновления

### Удобство использования
- Графический интерфейс с выбором языка (русский/английский)
- Наглядная информация о процессе обновления
- Автоматический запуск игры после обновления
- Обработка ошибок с информативными сообщениями

## Технические особенности

- Поддержка сжатия данных для эффективного хранения и передачи
- Клиент-серверная архитектура с возможностью масштабирования
- HTTP-download с поддержкой многопоточной загрузки
- Управление ресурсами для оптимального использования CPU и памяти

## Требования

- Go 1.19 или выше
- Fyne для GUI-клиента
- Доступ к хранилищу данных (Google Cloud Storage или подобное)

## Сборка

Для сборки клиента:
```bash
cd client-standalone
go build
```

Для сборки клиента Windows с помощью fyne-cross:
```bash
cd standalone
./build.sh
```

Для сборки сервера:
```bash
cd server
go build
```
