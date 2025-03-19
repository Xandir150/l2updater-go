#!/bin/bash
set -e

# Создаем каталог для релиза, если его нет
mkdir -p release

# Устанавливаем версию
VERSION="1.0.0"
echo "Building L2 Standalone Updater version $VERSION"

# Проверка и установка необходимых инструментов
check_and_install_tool() {
    local tool=$1
    local install_cmd=$2
    
    if ! command -v $tool &> /dev/null; then
        echo "$tool not found. Installing..."
        eval $install_cmd
        if ! command -v $tool &> /dev/null; then
            echo "Failed to install $tool. Skipping related build steps."
            return 1
        fi
    fi
    return 0
}

# Проверка наличия fyne-cross для сборки Windows-версии
FYNE_CROSS=$HOME/go/bin/fyne-cross
if ! check_and_install_tool "$FYNE_CROSS" "go install github.com/fyne-io/fyne-cross@latest"; then
    echo "fyne-cross is required for building. Aborting."
    exit 1
fi

# Сборка standalone-приложения (File Uploader с функцией очистки бакета) только для Windows
echo "Building Standalone File Uploader for Windows..."

echo "Building Standalone Uploader using fyne-cross..."
cd standalone
$FYNE_CROSS windows -app-id com.l2games.updater -app-version $VERSION -name "L2Updater-Standalone" -icon icon.png -arch=amd64 -tags disablegles31

# Копируем Windows-версию в release
if [ -f "fyne-cross/dist/windows-amd64/L2Updater-Standalone.zip" ]; then
    cp fyne-cross/dist/windows-amd64/L2Updater-Standalone.zip ../release/
    echo "Windows build completed successfully."
else
    echo "Windows build failed or output file not found."
fi

cd ..

echo "Build completed successfully!"
echo "Release files are available in the 'release' directory."

# Отображаем информацию о созданных файлах
echo "Created files:"
ls -la release/
