package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"cloud.google.com/go/storage"
	"context"
	"encoding/json"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"archive/zip"
	"bytes"
	
	// Библиотека для системных диалогов выбора файлов
	nativedialogs "github.com/sqweek/dialog"
)

const (
	DefaultBucketName = "client00.l2games.net"
	AESKeySize = 32 // 256 bit
	// Hidden encryption key, not exposed in UI
	DefaultEncryptionKey = "S86YyQO8E4M2CUeAMwoik04G"
)

// Uploader represents a file uploader.
type Uploader struct {
	sourceDir      string
	version        string
	bucketName     string
	client         *storage.Client
	encryptFiles   bool
	encryptionKey  string
	archiveFiles   bool
	manifestPath   string
	mutex          sync.Mutex
	concurrency    int
	semaphore      chan struct{}
	progressCb     func(int, int)
}

// File represents a file in the manifest.
type File struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash"`
	Modified int64  `json:"modified"`
	Archived bool   `json:"archived,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
}

// Manifest represents a manifest of files.
type Manifest struct {
	Version string           `json:"version"`
	Files   map[string]*File `json:"files"`
}

// GUILogWriter is a custom log writer that writes to the GUI log output
type GUILogWriter struct {
	textOutput *widget.Entry
}

// Write implements the io.Writer interface for the log writer
func (w *GUILogWriter) Write(p []byte) (n int, err error) {
	w.textOutput.SetText(w.textOutput.Text + string(p))
	return len(p), nil
}

// Process directory, upload files and create/update manifest
func (u *Uploader) ProcessDirectory() error {
	log.Println("Processing directory:", u.sourceDir)
	
	// Создаем новый манифест
	manifest := &Manifest{
		Version: u.version,
		Files:   make(map[string]*File),
	}
	
	// Сканирование директории и получение списка файлов
	files, err := u.scanDirectory(u.sourceDir)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %v", err)
	}
	
	log.Printf("Found %d files to process", len(files))
	
	// Загрузка файлов в Cloud Storage
	err = u.uploadFiles(files, manifest)
	if err != nil {
		return fmt.Errorf("failed to upload files: %v", err)
	}
	
	// Сохранение манифеста
	err = u.saveManifest(manifest)
	if err != nil {
		return fmt.Errorf("failed to save manifest: %v", err)
	}
	
	log.Println("All files processed successfully")
	return nil
}

// Scan directory and get list of files
func (u *Uploader) scanDirectory(dir string) ([]*File, error) {
	var files []*File
	
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		
		// Skip directories
		if info.IsDir() {
			return nil
		}
		
		// Get relative path
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		
		// Convert to forward slashes for consistent paths across platforms
		relPath = filepath.ToSlash(relPath)
		
		// Create file entry
		file := &File{
			Path:     relPath,
			Size:     info.Size(),
			Modified: info.ModTime().Unix(),
			Bucket:   u.bucketName,
		}
		
		// Calculate hash
		hash, err := calculateFileHash(path)
		if err != nil {
			return err
		}
		file.Hash = hash
		
		files = append(files, file)
		return nil
	})
	
	return files, err
}

// Calculate SHA-256 hash of a file
func calculateFileHash(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Upload files to Cloud Storage
func (u *Uploader) uploadFiles(files []*File, manifest *Manifest) error {
	ctx := context.Background()
	totalFiles := len(files)
	uploadedFiles := 0
	
	// Initialize the semaphore with the concurrency limit
	u.semaphore = make(chan struct{}, u.concurrency)
	
	var wg sync.WaitGroup
	
	// Process each file
	for _, file := range files {
		wg.Add(1)
		
		// Acquire a slot from the semaphore
		u.semaphore <- struct{}{}
		
		go func(file *File) {
			defer wg.Done()
			defer func() { <-u.semaphore }() // Release the semaphore slot when done
			
			// Get full path
			srcPath := filepath.Join(u.sourceDir, filepath.FromSlash(file.Path))
			
			// Generate object name (with obfuscation if needed)
			objectName := file.Path
			if u.archiveFiles {
				// Obfuscate filename
				objectName = obfuscateFileName(file.Path, u.encryptionKey)
				file.Archived = true
			}
			
			// Upload the file
			err := u.uploadFile(ctx, srcPath, objectName, file)
			if err != nil {
				log.Printf("Error uploading file %s: %v", file.Path, err)
				return
			}
			
			// Add file to manifest
			u.mutex.Lock()
			manifest.Files[file.Path] = file
			u.mutex.Unlock()
			
			// Update progress
			uploadedFiles++
			if u.progressCb != nil {
				u.progressCb(uploadedFiles, totalFiles)
			}
			
			log.Printf("Uploaded %s to %s", file.Path, objectName)
		}(file)
	}
	
	wg.Wait()
	
	return nil
}

// Upload a single file to Cloud Storage
func (u *Uploader) uploadFile(ctx context.Context, srcPath, objectName string, file *File) error {
	// Read the file
	data, err := ioutil.ReadFile(srcPath)
	if err != nil {
		return err
	}
	
	// Archive and encrypt if needed
	if u.archiveFiles {
		data, err = u.archiveAndEncrypt(file.Path, data)
		if err != nil {
			return err
		}
	}
	
	// Create a writer for the object
	bucket := u.client.Bucket(u.bucketName)
	obj := bucket.Object(objectName)
	w := obj.NewWriter(ctx)
	
	// Set metadata
	w.Metadata = map[string]string{
		"hash":     file.Hash,
		"path":     file.Path,
		"modified": fmt.Sprintf("%d", file.Modified),
	}
	
	// Write data
	if _, err := w.Write(data); err != nil {
		return err
	}
	
	// Close the writer
	if err := w.Close(); err != nil {
		return err
	}
	
	return nil
}

// Archive and encrypt a file
func (u *Uploader) archiveAndEncrypt(filePath string, data []byte) ([]byte, error) {
	// Create a buffer to write the zip to
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)
	
	// Create a new file in the zip
	zipFile, err := zipWriter.Create(filepath.Base(filePath))
	if err != nil {
		return nil, err
	}
	
	// Write the data to the file
	if _, err := zipFile.Write(data); err != nil {
		return nil, err
	}
	
	// Close the zip writer
	if err := zipWriter.Close(); err != nil {
		return nil, err
	}
	
	// If encryption is enabled, encrypt the archive
	if u.encryptFiles && u.encryptionKey != "" {
		return encryptData(buf.Bytes(), u.encryptionKey)
	}
	
	return buf.Bytes(), nil
}

// Save manifest to Cloud Storage
func (u *Uploader) saveManifest(manifest *Manifest) error {
	// Convert manifest to JSON
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	
	// Encrypt manifest data using the default encryption key
	encryptedData, err := encryptData(manifestData, DefaultEncryptionKey)
	if err != nil {
		return fmt.Errorf("failed to encrypt manifest: %v", err)
	}
	log.Printf("Manifest encrypted successfully")
	
	// Create manifest path
	manifestName := fmt.Sprintf("manifest-%s.json", manifest.Version)
	
	// Upload the manifest
	ctx := context.Background()
	bucket := u.client.Bucket(u.bucketName)
	obj := bucket.Object(manifestName)
	w := obj.NewWriter(ctx)
	
	// Write encrypted data
	if _, err := w.Write(encryptedData); err != nil {
		return err
	}
	
	// Close the writer
	if err := w.Close(); err != nil {
		return err
	}
	
	log.Printf("Uploaded encrypted manifest: %s", manifestName)
	
	// Also upload as latest manifest
	latestObj := bucket.Object("manifest-latest.json")
	w = latestObj.NewWriter(ctx)
	
	// Write encrypted data
	if _, err := w.Write(encryptedData); err != nil {
		return err
	}
	
	// Close the writer
	if err := w.Close(); err != nil {
		return err
	}
	
	log.Printf("Updated encrypted manifest-latest.json")
	
	return nil
}

// Obfuscate a filename using a key
func obfuscateFileName(filename, key string) string {
	// Generate a hash based on the filename and key
	h := sha256.New()
	h.Write([]byte(filename))
	h.Write([]byte(key))
	hash := h.Sum(nil)
	
	// Get file extension
	ext := filepath.Ext(filename)
	
	// Create obfuscated name with original extension
	return hex.EncodeToString(hash) + ext
}

// ClearBucket удаляет все файлы из бакета
func clearBucket(client *storage.Client, bucketName string, progressCb func(int, int)) error {
	ctx := context.Background()
	bucket := client.Bucket(bucketName)
	
	// Получаем все объекты в бакете
	var objects []*storage.ObjectAttrs
	it := bucket.Objects(ctx, nil)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("error listing objects: %v", err)
		}
		objects = append(objects, attrs)
	}
	
	log.Printf("Found %d objects to delete", len(objects))
	totalObjects := len(objects)
	deletedObjects := 0
	
	// Удаляем каждый объект
	for _, obj := range objects {
		if err := bucket.Object(obj.Name).Delete(ctx); err != nil {
			log.Printf("Error deleting object %s: %v", obj.Name, err)
			continue
		}
		
		deletedObjects++
		if progressCb != nil {
			progressCb(deletedObjects, totalObjects)
		}
		
		log.Printf("Deleted object: %s", obj.Name)
	}
	
	return nil
}

// Encrypt data using AES-256
func encryptData(data []byte, key string) ([]byte, error) {
	// Convert key to 32 bytes for AES-256
	keyBytes := sha256.Sum256([]byte(key))
	
	// Create cipher
	block, err := aes.NewCipher(keyBytes[:])
	if err != nil {
		return nil, err
	}
	
	// Create GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	
	// Create nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	
	// Encrypt data
	ciphertext := gcm.Seal(nonce, nonce, data, nil)
	
	return ciphertext, nil
}

// New application entry point
func main() {
	// Create a new app with software renderer
	a := app.NewWithID("com.l2games.updater")
	// Устанавливаем software renderer для лучшей совместимости
	_ = os.Setenv("FYNE_RENDERER", "software")
	a.Settings().SetTheme(theme.DarkTheme())
	
	w := a.NewWindow("L2 Updater - Upload Tool")
	w.Resize(fyne.NewSize(800, 600))
	
	// Create form fields
	sourceEntry := widget.NewEntry()
	sourceEntry.SetPlaceHolder("Path to source files")
	sourceButton := widget.NewButtonWithIcon("Browse", theme.FolderOpenIcon(), func() {
		// Используем системный диалог выбора директории для Windows
		dir, err := nativedialogs.Directory().Title("Select Source Directory").Browse()
		if err == nil && dir != "" {
			sourceEntry.SetText(dir)
		}
	})
	sourceContainer := container.NewBorder(nil, nil, nil, sourceButton, sourceEntry)
	
	credentialsEntry := widget.NewEntry()
	credentialsEntry.SetPlaceHolder("Path to Google Cloud credentials file")
	credentialsButton := widget.NewButtonWithIcon("Browse", theme.FolderOpenIcon(), func() {
		// Используем системный диалог выбора файла для Windows
		filePath, err := nativedialogs.File().
			Title("Select Google Cloud Credentials File").
			Filter("JSON Files", "json").
			Load()
		if err == nil && filePath != "" {
			credentialsEntry.SetText(filePath)
		}
	})
	credentialsContainer := container.NewBorder(nil, nil, nil, credentialsButton, credentialsEntry)
	
	bucketEntry := widget.NewEntry()
	bucketEntry.SetText(DefaultBucketName)
	bucketEntry.SetPlaceHolder("Google Cloud Storage bucket name")
	
	versionEntry := widget.NewEntry()
	versionEntry.SetText("1.0.0")
	versionEntry.SetPlaceHolder("Version")
	
	// Encryption key is now a constant and not exposed in the UI
	
	concurrencySelect := widget.NewSelect([]string{"1", "2", "3", "4", "5", "8", "10", "15", "20"}, nil)
	concurrencySelect.SetSelected("5")
	
	enableArchivingCheck := widget.NewCheck("Enable file archiving and obfuscation", nil)
	enableArchivingCheck.SetChecked(true)
	
	encryptFilesCheck := widget.NewCheck("Encrypt files", nil)
	encryptFilesCheck.SetChecked(true)
	
	updateModeCheck := widget.NewCheck("Update mode (only upload changed files)", nil)
	
	// Create log output
	logOutput := widget.NewMultiLineEntry()
	logOutput.Disable()
	logScroll := container.NewScroll(logOutput)
	logScroll.SetMinSize(fyne.NewSize(700, 200))
	
	// Progress bar
	progress := widget.NewProgressBar()
	progress.Hide()
	
	// Custom log writer to capture logs
	logWriter := &GUILogWriter{
		textOutput: logOutput,
	}
	log.SetOutput(logWriter)
	
	// Сначала объявляем все кнопки, чтобы избежать проблем с порядком объявления
	var uploadButton *widget.Button
	var clearBucketButton *widget.Button
	
	// Upload button
	uploadButton = widget.NewButtonWithIcon("Upload", theme.UploadIcon(), func() {
		// Validate fields
		if sourceEntry.Text == "" {
			dialog.ShowError(fmt.Errorf("source directory is required"), w)
			return
		}
		if credentialsEntry.Text == "" {
			dialog.ShowError(fmt.Errorf("credentials file is required"), w)
			return
		}
		if bucketEntry.Text == "" {
			dialog.ShowError(fmt.Errorf("bucket name is required"), w)
			return
		}
		
		// Using the built-in encryption key
		encryptionKey := DefaultEncryptionKey
		logOutput.SetText(logOutput.Text + "Using built-in encryption key\n")
		
		// Disable UI during upload
		setUIEnabled(false, sourceEntry, sourceButton, credentialsEntry, credentialsButton,
			bucketEntry, versionEntry, concurrencySelect,
			enableArchivingCheck, encryptFilesCheck, updateModeCheck)
		
		progress.Show()
		progress.SetValue(0)
		
		// Start upload process in a goroutine
		go func() {
			defer func() {
				setUIEnabled(true, sourceEntry, sourceButton, credentialsEntry, credentialsButton,
					bucketEntry, versionEntry, concurrencySelect,
					enableArchivingCheck, encryptFilesCheck, updateModeCheck)
				progress.Hide()
			}()
			
			// Process the upload using integrated uploader
			logOutput.SetText(logOutput.Text + "Starting upload process...\n")
			
			// Parse concurrency
			concurrency := 5
			fmt.Sscanf(concurrencySelect.Selected, "%d", &concurrency)
			if concurrency < 1 {
				concurrency = 5
			}
			
			// Create GCS client
			ctx := context.Background()
			client, err := storage.NewClient(ctx, option.WithCredentialsFile(credentialsEntry.Text))
			if err != nil {
				// Используем нативный диалог ошибки
				nativedialogs.Message("%s", fmt.Sprintf("Failed to create GCS client: %v", err)).Title("Error").Error()
				logOutput.SetText(logOutput.Text + fmt.Sprintf("Error: %v\n", err))
				return
			}
			defer client.Close()
			
			// Create uploader
			uploader := &Uploader{
				sourceDir:     sourceEntry.Text,
				version:       versionEntry.Text,
				bucketName:    bucketEntry.Text,
				client:        client,
				encryptFiles:  encryptFilesCheck.Checked,
				encryptionKey: encryptionKey,
				archiveFiles:  enableArchivingCheck.Checked,
				concurrency:   concurrency,
				progressCb: func(current, total int) {
					progress.SetValue(float64(current) / float64(total))
				},
			}
			
			// Process directory
			if err := uploader.ProcessDirectory(); err != nil {
				// Используем нативный диалог ошибки
				nativedialogs.Message("%s", fmt.Sprintf("Upload failed: %v", err)).Title("Error").Error()
				logOutput.SetText(logOutput.Text + fmt.Sprintf("Error: %v\n", err))
				return
			}
			
			logOutput.SetText(logOutput.Text + "Upload completed successfully!\n")
			
			// Show success dialog
			dialog.ShowInformation("Success", "Upload completed successfully!", w)
		}()
	})
	
	// Layout form fields
	form := container.NewVBox(
		widget.NewLabel("Source Settings"),
		container.NewVBox(
			widget.NewLabel("Source Directory:"),
			sourceContainer,
		),
		widget.NewLabel("Google Cloud Storage Settings"),
		container.NewVBox(
			widget.NewLabel("GCS Credentials File:"),
			credentialsContainer,
			widget.NewLabel("Bucket Name:"),
			bucketEntry,
		),
		widget.NewLabel("Upload Settings"),
		container.NewVBox(
			widget.NewLabel("Version:"),
			versionEntry,
			// Encryption key is now a constant
			widget.NewLabel("Concurrent Uploads:"),
			concurrencySelect,
			enableArchivingCheck,
			encryptFilesCheck,
			updateModeCheck,
		),
	)
	
	// Create clear log button
	clearLogButton := widget.NewButtonWithIcon("Clear Log", theme.DeleteIcon(), func() {
		logOutput.SetText("")
	})
	
	// Create clear bucket button
	clearBucketButton = widget.NewButtonWithIcon("Clear Bucket", theme.ContentClearIcon(), func() {
		// Проверяем, заполнены ли необходимые поля
		if credentialsEntry.Text == "" {
			nativedialogs.Message("%s", "Credentials file is required").Title("Error").Error()
			return
		}
		if bucketEntry.Text == "" {
			nativedialogs.Message("%s", "Bucket name is required").Title("Error").Error()
			return
		}
		
		// Показываем нативный диалог подтверждения Windows
		confirmed := nativedialogs.Message("%s", "WARNING: This will DELETE ALL FILES in the bucket. This action cannot be undone. Are you sure you want to proceed?").Title("Confirm Bucket Cleanup").YesNo()
		if !confirmed {
			return
		}
		
		// Отключаем интерфейс и показываем прогресс
		setUIEnabled(false, sourceEntry, sourceButton, credentialsEntry, credentialsButton,
			bucketEntry, versionEntry, concurrencySelect,
			enableArchivingCheck, encryptFilesCheck, updateModeCheck, clearBucketButton)
		
		progress.Show()
		progress.SetValue(0)
		
		// Запускаем процесс очистки в отдельной горутине
		go func() {
			defer func() {
				setUIEnabled(true, sourceEntry, sourceButton, credentialsEntry, credentialsButton,
					bucketEntry, versionEntry, concurrencySelect,
					enableArchivingCheck, encryptFilesCheck, updateModeCheck, clearBucketButton)
				progress.Hide()
			}()
			
			logOutput.SetText(logOutput.Text + "Starting bucket cleanup...\n")
			
			// Создаем клиент GCS
			ctx := context.Background()
			client, err := storage.NewClient(ctx, option.WithCredentialsFile(credentialsEntry.Text))
			if err != nil {
				nativedialogs.Message("%s", fmt.Sprintf("Failed to create GCS client: %v", err)).Title("Error").Error()
				logOutput.SetText(logOutput.Text + fmt.Sprintf("Error: %v\n", err))
				return
			}
			defer client.Close()
			
			// Очищаем бакет
			if err := clearBucket(client, bucketEntry.Text, func(current, total int) {
				progress.SetValue(float64(current) / float64(total))
			}); err != nil {
				nativedialogs.Message("%s", fmt.Sprintf("Bucket cleanup failed: %v", err)).Title("Error").Error()
				logOutput.SetText(logOutput.Text + fmt.Sprintf("Error: %v\n", err))
				return
			}
			
			logOutput.SetText(logOutput.Text + "Bucket cleanup completed successfully!\n")
			
			// Показываем нативный диалог успеха Windows
			nativedialogs.Message("%s", "Bucket cleanup completed successfully!").Title("Success").Info()
		}()
	})
	
	// Main layout
	buttonBox := container.NewHBox(uploadButton, layout.NewSpacer(), clearBucketButton)
	
	content := container.NewVBox(
		container.NewPadded(form),
		container.NewPadded(buttonBox),
		container.NewHBox(
			widget.NewLabel("Log Output:"),
			layout.NewSpacer(),
			clearLogButton,
		),
		progress,
		logScroll,
	)
	
	// Set content and show window
	w.SetContent(container.NewPadded(content))
	w.ShowAndRun()
}

// setUIEnabled enables or disables UI elements during upload
func setUIEnabled(enabled bool, controls ...fyne.CanvasObject) {
	for _, control := range controls {
		switch c := control.(type) {
		case *widget.Entry:
			if enabled {
				c.Enable()
			} else {
				c.Disable()
			}
		case *widget.Button:
			if enabled {
				c.Enable()
			} else {
				c.Disable()
			}
			c.Refresh()
		case *widget.Check:
			if enabled {
				c.Enable()
			} else {
				c.Disable()
			}
			c.Refresh()
		case *widget.Select:
			if enabled {
				c.Enable()
			} else {
				c.Disable()
			}
		}
	}
}

// generateEncryptionKey generates a random encryption key
func generateEncryptionKey() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, 24) // 24 characters for the key
	for i := range result {
		result[i] = chars[time.Now().UnixNano()%int64(len(chars))]
		time.Sleep(time.Nanosecond)
	}
	return string(result)
}
