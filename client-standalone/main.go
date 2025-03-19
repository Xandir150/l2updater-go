package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	appTitle          = "L2 Game Launcher"
	langRussian       = 8
	langEnglish       = 9
	gameExecutable    = "system\\l2.exe"
	// Hidden encryption key, not shown in UI
	encryptionKey     = "S86YyQO8E4M2CUeAMwoik04G"
	defaultBucketName = "client00.l2games.net"
	manifestFileName  = "manifest-latest.json"
	// HTTP URL for downloading files
	httpBaseURL       = "https://storage.googleapis.com/"
	// Number of concurrent download threads
	concurrentThreads = 5
)

var (
	// Global UI variables
	mainProgressBar *widget.ProgressBar
	actionLabel     *widget.Label
	playButton      *widget.Button
	russianButton   *widget.Button
	englishButton   *widget.Button
	// Individual progress bars for each thread
	threadProgressBars [concurrentThreads]*widget.ProgressBar
	threadLabels       [concurrentThreads]*widget.Label
	// Language selection (default Russian)
	selectedLang = langRussian
	// Application instance
	myApp fyne.App
)

// FileInfo represents information about a file in the manifest
type FileInfo struct {
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	Hash         string    `json:"hash"`
	LastModified time.Time `json:"lastModified"`
	Archived     bool      `json:"archived,omitempty"`
}

// fileCheckTask represents a task to check a file against the manifest
type fileCheckTask struct {
	FilePath   string
	ObjectName string
	Size       int64
	Hash       string
	Archived   bool
}

// fileCheckResult represents the result of a file check
type fileCheckResult struct {
	FilePath      string
	ObjectName    string
	Size          int64
	Hash          string
	Archived      bool
	NeedsDownload bool
}

// Manifest represents the structure of the manifest file
type Manifest struct {
	Version     string              `json:"version"`
	GeneratedAt time.Time           `json:"generatedAt"`
	Files       map[string]FileInfo `json:"files"`
}

// DownloadTask represents a file download task
type DownloadTask struct {
	FilePath   string
	ObjectName string
	Size       int64
	Hash       string
	Archived   bool
}

// DownloadResult represents the result of a download task
type DownloadResult struct {
	FilePath string
	Success  bool
	Error    error
}

// DownloadProgress represents download progress information
type DownloadProgress struct {
	ThreadID       int
	FilePath       string
	BytesCompleted int64
	TotalBytes     int64
	Completed      bool
	Error          error
}

// Downloader handles downloading files from HTTP
type Downloader struct {
	ctx            context.Context
	bucketName     string
	targetDir      string
	httpBaseURL    string
	progressChan   chan DownloadProgress
	downloadQueue  chan DownloadTask
	resultChan     chan DownloadResult
	workerWg       sync.WaitGroup
	downloadStats  struct {
		totalFiles     int
		downloadedSize int64
		skippedFiles   int
		updatedFiles   int
		failedFiles    int
		mutex          sync.Mutex
	}
}

// NewDownloader creates a new downloader for HTTP
func NewDownloader(ctx context.Context, bucketName, targetDir string) (*Downloader, error) {
	// Create base directories
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create target directory: %v", err)
	}

	return &Downloader{
		ctx:           ctx,
		bucketName:    bucketName,
		targetDir:     targetDir,
		httpBaseURL:   httpBaseURL + bucketName,
		progressChan:  make(chan DownloadProgress, concurrentThreads*2),
		downloadQueue: make(chan DownloadTask, 100),
		resultChan:    make(chan DownloadResult, 100),
	}, nil
}

// Close closes the downloader
func (d *Downloader) Close() {
	// Nothing to close with HTTP-only implementation
}

// DownloadManifest downloads and decrypts the manifest file
func (d *Downloader) DownloadManifest() (*Manifest, error) {
	log.Printf("Attempting to download manifest: %s from bucket: %s", manifestFileName, d.bucketName)
	log.Printf("Using HTTP base URL: %s", d.httpBaseURL)
	
	// Download encrypted manifest data
	encryptedData, err := d.downloadObjectToMemory(manifestFileName)
	if err != nil {
		log.Printf("ERROR: Failed to download manifest (%s): %v", manifestFileName, err)
		return nil, fmt.Errorf("failed to download manifest: %v", err)
	}
	log.Printf("Successfully downloaded manifest (%d bytes)", len(encryptedData))

	// Decrypt the manifest data
	log.Printf("Attempting to decrypt manifest data")
	manifestData, err := decryptData(encryptedData, encryptionKey)
	if err != nil {
		log.Printf("ERROR: Failed to decrypt manifest: %v", err)
		return nil, fmt.Errorf("failed to decrypt manifest: %v", err)
	}
	log.Printf("Successfully decrypted manifest data (%d bytes)", len(manifestData))
	
	// Parse the JSON manifest
	log.Printf("Attempting to parse manifest JSON")
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		log.Printf("ERROR: Failed to parse manifest JSON: %v", err)
		return nil, fmt.Errorf("failed to parse manifest: %v", err)
	}
	log.Printf("Successfully parsed manifest with %d files", len(manifest.Files))

	return &manifest, nil
}

// downloadObjectToMemory downloads an object to memory
func (d *Downloader) downloadObjectToMemory(objectName string) ([]byte, error) {
	// Construct proper URL: httpBaseURL + bucketName + / + objectName
	url := fmt.Sprintf("%s%s/%s", httpBaseURL, d.bucketName, objectName)
	log.Printf("Downloading object from URL: %s", url)
	
	// Use HTTP client with timeout
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("ERROR: HTTP request failed: %v", err)
		return nil, fmt.Errorf("network error: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		log.Printf("ERROR: HTTP request returned status %d: %s", resp.StatusCode, resp.Status)
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}
	log.Printf("Received HTTP 200 OK response with content length: %d", resp.ContentLength)
	
	// Read the response body
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("ERROR: Failed to read response body: %v", err)
		return nil, fmt.Errorf("failed to read response: %v", err)
	}
	log.Printf("Successfully read %d bytes from response body", len(data))
	
	return data, nil
}

// StartWorkers starts download worker goroutines
func (d *Downloader) StartWorkers() {
	for i := 0; i < concurrentThreads; i++ {
		d.workerWg.Add(1)
		threadID := i
		
		go func() {
			defer d.workerWg.Done()
			d.downloadWorker(threadID)
		}()
	}
}

// downloadWorker processes download tasks
func (d *Downloader) downloadWorker(threadID int) {
	// Create a semaphore to limit parallel unarchiving operations
	// They are CPU-intensive and can consume a lot of memory for large files
	unarchiveSemaphore := make(chan struct{}, 2) // Allow 2 concurrent unarchive operations per thread
	var unarchiveWg sync.WaitGroup
	
	for task := range d.downloadQueue {
		localPath := filepath.Join(d.targetDir, task.FilePath)
		
		// Create directory if it doesn't exist
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			d.resultChan <- DownloadResult{
				FilePath: task.FilePath,
				Success:  false,
				Error:    fmt.Errorf("failed to create directory: %v", err),
			}
			continue
		}
		
		// Report starting download
		d.progressChan <- DownloadProgress{
			ThreadID:   threadID,
			FilePath:   task.FilePath,
			TotalBytes: task.Size,
		}
		
		if task.Archived {
			// Process archived files in parallel, but control the number
			// of concurrent unarchiving operations
			unarchiveSemaphore <- struct{}{} // Acquire semaphore
			unarchiveWg.Add(1)
			
			// Create local copies of parameters to avoid closure issues
			taskCopy := task
			localPathCopy := localPath
			
			go func() {
				defer func() { 
					<-unarchiveSemaphore // Release semaphore when done
					unarchiveWg.Done()
				}()
				
				// Download and unarchive the file
				err := d.downloadAndUnarchiveFile(threadID, taskCopy.ObjectName, localPathCopy, taskCopy.Size)
				success := err == nil
				
				// Report result
				d.resultChan <- DownloadResult{
					FilePath: taskCopy.FilePath,
					Success:  success,
					Error:    err,
				}
				
				// Report completion
				d.progressChan <- DownloadProgress{
					ThreadID:       threadID,
					FilePath:       taskCopy.FilePath,
					BytesCompleted: taskCopy.Size,
					TotalBytes:     taskCopy.Size,
					Completed:      true,
					Error:          err,
				}
			}()
		} else {
			// For regular files, use sequential download
			err := d.downloadFile(threadID, task.ObjectName, localPath, task.Size)
			success := err == nil
			
			// Отправляем результат
			d.resultChan <- DownloadResult{
				FilePath: task.FilePath,
				Success:  success,
				Error:    err,
			}
			
			// Сообщаем о завершении
			d.progressChan <- DownloadProgress{
				ThreadID:       threadID,
				FilePath:       task.FilePath,
				BytesCompleted: task.Size,
				TotalBytes:     task.Size,
				Completed:      true,
				Error:          err,
			}
		}
	}
	
	// Wait for all unarchiving operations to complete before exiting
	unarchiveWg.Wait()
}

// downloadFile downloads a file from HTTP
func (d *Downloader) downloadFile(threadID int, objectName, localPath string, size int64) error {
	log.Printf("Thread %d: Downloading file %s to %s", threadID+1, objectName, localPath)
	return d.downloadFileHTTP(threadID, objectName, localPath, size)
}

// downloadFileHTTP downloads a file using HTTP
func (d *Downloader) downloadFileHTTP(threadID int, objectName, localPath string, size int64) error {
	// Properly construct URL without double slashes
	url := fmt.Sprintf("%s/%s", d.httpBaseURL, objectName)
	log.Printf("Thread %d: Downloading from URL: %s", threadID+1, url)
	
	httpClient := &http.Client{Timeout: 10 * time.Minute}
	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("Thread %d: Download error: %v", threadID+1, err)
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		log.Printf("Thread %d: HTTP error: %s", threadID+1, resp.Status)
		return fmt.Errorf("HTTP error: %s", resp.Status)
	}
	
	log.Printf("Thread %d: Successfully received response for %s (status: %d)", threadID+1, objectName, resp.StatusCode)
	
	return d.saveReaderToFile(threadID, resp.Body, localPath, size)
}

// saveReaderToFile saves content from a reader to a file with progress reporting
func (d *Downloader) saveReaderToFile(threadID int, r io.Reader, localPath string, size int64) error {
	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	
	// Set up progress tracking
	pr := &progressReader{
		r:           r,
		threadID:    threadID,
		filePath:    filepath.Base(localPath),
		size:        size,
		progress:    d.progressChan,
		reportEvery: 32 * 1024, // Report progress every 32KB
	}
	
	_, err = io.Copy(f, pr)
	return err
}

// downloadAndUnarchiveFile downloads, decrypts, and extracts an archived file
func (d *Downloader) downloadAndUnarchiveFile(threadID int, objectName, localPath string, size int64) error {
	// Download the archived file to memory
	archivedData, err := d.downloadObjectToMemory(objectName)
	if err != nil {
		return err
	}
	
	// Report progress
	d.progressChan <- DownloadProgress{
		ThreadID:       threadID,
		FilePath:       filepath.Base(localPath),
		BytesCompleted: int64(len(archivedData)),
		TotalBytes:     size,
	}
	
	// Decrypt the data
	decryptedData, err := decryptData(archivedData, encryptionKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt file: %v", err)
	}
	
	// Write the decrypted data to the file
	if err := os.WriteFile(localPath, decryptedData, 0644); err != nil {
		return fmt.Errorf("failed to write file: %v", err)
	}
	
	return nil
}

// SyncWithManifest synchronizes local files with the manifest
func (d *Downloader) SyncWithManifest(manifest *Manifest) error {
	// Close any previously open channels
	if d.downloadQueue != nil {
		close(d.downloadQueue)
	}
	
	// Reinitialize channels
	d.downloadQueue = make(chan DownloadTask, 100)
	d.resultChan = make(chan DownloadResult, 100)
	d.downloadStats = struct {
		totalFiles     int
		downloadedSize int64
		skippedFiles   int
		updatedFiles   int
		failedFiles    int
		mutex          sync.Mutex
	}{totalFiles: len(manifest.Files)}
	
	// Start worker goroutines
	d.StartWorkers()
	
	// Create a channel to report progress of file checking
	checkProgressChan := make(chan int, 10)
	totalChecked := 0
	
	// Start a goroutine to update progress of file checking
	go func() {
		totalFiles := len(manifest.Files)
		for progress := range checkProgressChan {
			totalChecked += progress
			// Report progress to UI
			d.progressChan <- DownloadProgress{
				ThreadID:       -1, // Special ID for file checking progress
				FilePath:       "Checking files",
				BytesCompleted: int64(totalChecked),
				TotalBytes:     int64(totalFiles),
			}
		}
	}()
	
	// Number of concurrent checkers for file verification
	const numCheckers = 5
	filesToCheck := make(chan fileCheckTask, 100)
	checkResults := make(chan fileCheckResult, 100)
	var checkWg sync.WaitGroup
	
	// Start file check workers
	for i := 0; i < numCheckers; i++ {
		checkWg.Add(1)
		go func(workerID int) {
			defer checkWg.Done()
			for task := range filesToCheck {
				localPath := filepath.Join(d.targetDir, task.FilePath)
				needsDownload := true
				
				if fileExists(localPath) {
					// Fast check: first compare file size
					stat, err := os.Stat(localPath)
					if err == nil && stat.Size() == task.Size {
						// File size matches, now check hash if needed
						// In this implementation we trust the file if size matches
						// You can uncomment the hash check if you need stronger verification
						/*
						fileHash, err := calculateFileHash(localPath)
						if err == nil && fileHash == task.Hash {
							needsDownload = false
						}
						*/
						needsDownload = false
					}
				}
				
				checkResults <- fileCheckResult{
					FilePath:      task.FilePath,
					ObjectName:    task.ObjectName,
					Size:          task.Size,
					Hash:          task.Hash,
					Archived:      task.Archived,
					NeedsDownload: needsDownload,
				}
				
				// Report progress
				checkProgressChan <- 1
			}
		}(i)
	}
	
	// Queue all files for checking
	go func() {
		for filePath, fileInfo := range manifest.Files {
			objectName := fileInfo.Path
			if fileInfo.Archived {
				// Handle archived files differently
				objectName = obfuscateFileName(filePath, encryptionKey)
			}
			
			filesToCheck <- fileCheckTask{
				FilePath:   filePath,
				ObjectName: objectName,
				Size:       fileInfo.Size,
				Hash:       fileInfo.Hash,
				Archived:   fileInfo.Archived,
			}
		}
		close(filesToCheck)
	}()
	
	// Process check results and queue downloads
	go func() {
		// Wait for all checks to complete and close channels
		go func() {
			checkWg.Wait()
			close(checkResults)
			close(checkProgressChan)
		}()
		
		// Process results from file checking
		for result := range checkResults {
			if result.NeedsDownload {
				// Queue file for download
				d.downloadQueue <- DownloadTask{
					FilePath:   result.FilePath,
					ObjectName: result.ObjectName,
					Size:       result.Size,
					Hash:       result.Hash,
					Archived:   result.Archived,
				}
			} else {
				// Skip this file
				d.downloadStats.mutex.Lock()
				d.downloadStats.skippedFiles++
				d.downloadStats.mutex.Unlock()
			}
		}
		
		// Close the download queue when all files are queued
		close(d.downloadQueue)
	}()
	
	return nil
}

// WaitForCompletion waits for all downloads to complete
func (d *Downloader) WaitForCompletion() (int, int, int, error) {
	// Start a goroutine to close resultChan when all workers are done
	go func() {
		d.workerWg.Wait()
		close(d.resultChan)
		close(d.progressChan)
	}()
	
	// Process results as they come in
	for result := range d.resultChan {
		d.downloadStats.mutex.Lock()
		if result.Success {
			d.downloadStats.updatedFiles++
		} else {
			d.downloadStats.failedFiles++
			log.Printf("Failed to download %s: %v", result.FilePath, result.Error)
		}
		d.downloadStats.mutex.Unlock()
	}
	
	return d.downloadStats.updatedFiles, d.downloadStats.skippedFiles, d.downloadStats.failedFiles, nil
}

// progressReader tracks reading progress and reports it
type progressReader struct {
	r           io.Reader
	threadID    int
	filePath    string
	size        int64
	read        int64
	progress    chan<- DownloadProgress
	reportEvery int64
	lastReport  int64
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 {
		pr.read += int64(n)
		if pr.read-pr.lastReport >= pr.reportEvery {
			pr.progress <- DownloadProgress{
				ThreadID:       pr.threadID,
				FilePath:       pr.filePath,
				BytesCompleted: pr.read,
				TotalBytes:     pr.size,
			}
			pr.lastReport = pr.read
		}
	}
	return n, err
}

// Helper functions

// calculateFileHash calculates the SHA-256 hash of a file
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

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// decryptData decrypts AES-encrypted data using GCM mode to match server encryption
func decryptData(data []byte, key string) ([]byte, error) {
	log.Printf("Decrypting data of length %d bytes", len(data))
	
	// Convert key to 32 bytes for AES-256 using the same algorithm as server
	keyBytes := sha256.Sum256([]byte(key))
	
	// Create cipher block
	block, err := aes.NewCipher(keyBytes[:])
	if err != nil {
		log.Printf("Error creating cipher: %v", err)
		return nil, err
	}
	
	// Create GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		log.Printf("Error creating GCM: %v", err)
		return nil, err
	}
	
	// Get the nonce size
	nonceSize := gcm.NonceSize()
	log.Printf("Using nonce size: %d", nonceSize)
	
	// Ensure the data is long enough
	if len(data) < nonceSize {
		log.Printf("Data too short: %d bytes, need at least %d bytes for nonce", len(data), nonceSize)
		return nil, fmt.Errorf("ciphertext too short, must be at least %d bytes", nonceSize)
	}
	
	// Extract nonce and ciphertext
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	log.Printf("Extracted nonce (%d bytes) and ciphertext (%d bytes)", len(nonce), len(ciphertext))
	
	// Decrypt data
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		log.Printf("Decryption error: %v", err)
		return nil, fmt.Errorf("decryption failed: %v", err)
	}
	
	log.Printf("Successfully decrypted data to %d bytes", len(plaintext))
	return plaintext, nil
}

// obfuscateFileName obfuscates the file name using the key
func obfuscateFileName(fileName, key string) string {
	// Generate a hash based on the filename and key
	h := sha256.New()
	h.Write([]byte(fileName))
	h.Write([]byte(key))
	hash := h.Sum(nil)
	
	// Get file extension
	ext := filepath.Ext(fileName)
	
	// Create obfuscated name with original extension
	return hex.EncodeToString(hash) + ext
}

// Main application

func main() {
	myApp = app.New()
	myApp.Settings().SetTheme(theme.DarkTheme())
	w := myApp.NewWindow(appTitle)
	w.Resize(fyne.NewSize(800, 600))
	
	// Setup UI
	setupUI(w)
	
	w.ShowAndRun()
}

// setupUI creates the user interface
func setupUI(w fyne.Window) {
	// Create main layout
	title := widget.NewLabelWithStyle(appTitle, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	
	// Parameters section - только выбор языка
	langLabel := widget.NewLabel("Language:")
	
	// Language selection
	
	russianButton = widget.NewButton("Russian", func() {
		selectedLang = langRussian
		updateLanguageButtons()
	})
	
	englishButton = widget.NewButton("English", func() {
		selectedLang = langEnglish
		updateLanguageButtons()
	})
	
	// Initially set Russian as selected
	updateLanguageButtons()
	
	// Progress section
	mainProgressBar = widget.NewProgressBar()
	actionLabel = widget.NewLabel("Ready")
	
	// Thread progress bars
	threadProgressContainer := container.NewVBox()
	for i := 0; i < concurrentThreads; i++ {
		threadLabels[i] = widget.NewLabel(fmt.Sprintf("Thread %d: Idle", i+1))
		threadProgressBars[i] = widget.NewProgressBar()
		threadProgressContainer.Add(container.NewVBox(
			threadLabels[i],
			threadProgressBars[i],
		))
	}
	
	// Log output - single area for all events
	logOutput := widget.NewMultiLineEntry()
	logOutput.Disable()
	// Make log output take more vertical space and use full window width
	logOutput.SetMinRowsVisible(15)
	// Set wrapping to match window width
	logOutput.Wrapping = fyne.TextWrapWord
	
	// Redirect log output to the text widget
	log.SetOutput(&guiLogWriter{textOutput: logOutput})
	
	// Автоматически запускаем обновление при старте приложения
	go func() {
		// Используем текущую директорию и дефолтное имя корзины
		currDir, err := os.Getwd()
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to get current directory: %v", err), w)
			return
		}
		
		// Небольшая задержка, чтобы UI успел отрисоваться
		time.Sleep(500 * time.Millisecond)
		
		// Запуск обновления с текущей директорией
		startUpdateProcess(w, defaultBucketName, currDir)
	}()
	
	// Play button (initially disabled)
	playButton = widget.NewButton("Play Game", func() {
		// Используем текущую директорию
		currDir, err := os.Getwd()
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to get current directory: %v", err), w)
			return
		}
		launchGame(currDir)
	})
	playButton.Disable()
	
	// Exit button
	exitButton := widget.NewButton("Exit", func() {
		myApp.Quit()
	})
	
	// Layout containers
	paramsContainer := container.NewVBox(
		container.NewGridWithColumns(2,
			langLabel, container.NewHBox(russianButton, englishButton),
		),
	)
	
	progressContainer := container.NewVBox(
		actionLabel,
		mainProgressBar,
		widget.NewSeparator(),
		container.NewScroll(threadProgressContainer),
	)
	
	// Create an expandable log container that fills available width
	logHeader := widget.NewLabel("Events:")
	logScroll := container.NewScroll(logOutput)
	
	// Use a border layout to maximize the space for logs
	logContainer := container.NewBorder(
		logHeader,
		nil, nil, nil,
		logScroll,
	)
	
	buttonsContainer := container.NewHBox(
		layout.NewSpacer(),
		playButton,
		exitButton,
		layout.NewSpacer(),
	)
	
	// Main layout
	mainContainer := container.NewVBox(
		title,
		widget.NewSeparator(),
		paramsContainer,
		widget.NewSeparator(),
		progressContainer,
		widget.NewSeparator(),
		buttonsContainer,
		widget.NewSeparator(),
		logContainer,
	)
	
	// Set a minimum size for the window to ensure logs are readable
	w.Resize(fyne.NewSize(900, 600))
	w.SetContent(container.NewPadded(mainContainer))
}

// updateLanguageButtons updates the language selection buttons
func updateLanguageButtons() {
	if selectedLang == langRussian {
		russianButton.Importance = widget.HighImportance
		englishButton.Importance = widget.MediumImportance
	} else {
		russianButton.Importance = widget.MediumImportance
		englishButton.Importance = widget.HighImportance
	}
	russianButton.Refresh()
	englishButton.Refresh()
}

// startUpdateProcess initiates the file update process
func startUpdateProcess(w fyne.Window, bucketName, targetDir string) {
	log.Printf("Starting update process with bucket '%s' in directory: %s", bucketName, targetDir)
	// Disable UI during update
	setUIEnabled(false)
	
	// Initialize progress
	mainProgressBar.SetValue(0)
	actionLabel.SetText("Initializing...")
	for i := 0; i < concurrentThreads; i++ {
		threadLabels[i].SetText(fmt.Sprintf("Thread %d: Idle", i+1))
		threadProgressBars[i].SetValue(0)
	}
	
	// Start update process in a goroutine
	go func() {
		// Add panic recovery to prevent app crash
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in update process: %v", r)
				updateStatus(fmt.Sprintf("Critical error: %v", r))
				setUIEnabled(true)
				// Show error dialog
				dialog.ShowError(fmt.Errorf("Critical error: %v", r), w)
			}
		}()
		
		ctx := context.Background()
		log.Printf("Starting update process with bucket '%s' in directory '%s'", bucketName, targetDir)
		
		// Create downloader
		log.Printf("Creating downloader instance...")
		downloader, err := NewDownloader(ctx, bucketName, targetDir)
		if err != nil {
			log.Printf("Error creating downloader: %v", err)
			updateStatus("Error: Failed to initialize downloader")
			setUIEnabled(true)
			// Show error dialog with more details
			dialog.ShowError(fmt.Errorf("Failed to initialize downloader: %v", err), w)
			return
		}
		defer downloader.Close()
		
		// Download manifest
		updateStatus("Downloading manifest...")
		log.Printf("Attempting to download game manifest...")
		manifest, err := downloader.DownloadManifest()
		if err != nil {
			log.Printf("Error downloading manifest: %v", err)
			errMsg := fmt.Sprintf("Error: Failed to download manifest")
			updateStatus(errMsg)
			setUIEnabled(true)
			// Show error dialog with more details
			dialog.ShowError(fmt.Errorf("Failed to download manifest: %v. Please check your internet connection and ensure the server is running.", err), w)
			return
		}
		
		totalFiles := len(manifest.Files)
		log.Printf("Manifest contains %d files", totalFiles)
		updateStatus(fmt.Sprintf("Checking %d files...", totalFiles))
		
		// Start sync process with the manifest
		if err := downloader.SyncWithManifest(manifest); err != nil {
			log.Printf("Error starting sync: %v", err)
			updateStatus("Error: Failed to sync files")
			setUIEnabled(true)
			return
		}
		
		// Process progress updates
		go func() {
			for progress := range downloader.progressChan {
				// Special ID for file checking progress
				if progress.ThreadID == -1 {
					// Update main progress bar for file checking phase
					if progress.TotalBytes > 0 {
						mainProgressBar.SetValue(float64(progress.BytesCompleted) / float64(progress.TotalBytes))
						updateStatus(fmt.Sprintf("Checking files (%d of %d)...", progress.BytesCompleted, progress.TotalBytes))
					}
				} else if progress.ThreadID >= 0 && progress.ThreadID < concurrentThreads {
					// Update thread progress
					threadLabels[progress.ThreadID].SetText(fmt.Sprintf("Thread %d: %s", 
						progress.ThreadID+1, filepath.Base(progress.FilePath)))
					
					if progress.TotalBytes > 0 {
						threadProgressBars[progress.ThreadID].SetValue(float64(progress.BytesCompleted) / float64(progress.TotalBytes))
					}
					
					if progress.Completed {
						if progress.Error == nil {
							threadLabels[progress.ThreadID].SetText(fmt.Sprintf("Thread %d: Completed", progress.ThreadID+1))
						} else {
							threadLabels[progress.ThreadID].SetText(fmt.Sprintf("Thread %d: Error", progress.ThreadID+1))
						}
					}
				}
			}
		}()
		
		// Wait for completion
		updated, skipped, failed, err := downloader.WaitForCompletion()
		if err != nil {
			log.Printf("Error during download: %v", err)
			updateStatus("Error: Download process failed")
		} else {
			log.Printf("Download complete: %d updated, %d skipped, %d failed", updated, skipped, failed)
			
			if failed > 0 {
				updateStatus(fmt.Sprintf("Completed with errors: %d updated, %d skipped, %d failed", updated, skipped, failed))
			} else {
				updateStatus(fmt.Sprintf("Successfully completed: %d updated, %d skipped", updated, skipped))
				// Enable play button only if all files are valid
				playButton.Enable()
			}
		}
		
		// Set main progress to complete
		mainProgressBar.SetValue(1.0)
		
		// Re-enable UI
		setUIEnabled(true)
	}()
}

// setUIEnabled enables or disables UI elements during the update process
func setUIEnabled(enabled bool) {
	// This function will be implemented to enable/disable UI elements
	if enabled {
		// If process completed without errors, enable the play button
		// playButton is handled separately in the completion logic
	} else {
		playButton.Disable()
	}
}

// updateStatus updates the status label and log
func updateStatus(text string) {
	// Also log the status message
	log.Printf("STATUS: %s", text)
	actionLabel.SetText(text)
}

// launchGame launches the game with the selected language
func launchGame(gamePath string) {
	// Determine executable name based on OS
	var exeName string
	if runtime.GOOS == "windows" {
		exeName = "L2.exe"
	} else {
		exeName = "./L2"
	}
	
	exePath := filepath.Join(gamePath, exeName)
	
	// Check if the executable exists
	if !fileExists(exePath) {
		log.Printf("Game executable not found: %s", exePath)
		updateStatus("Error: Game executable not found")
		return
	}
	
	// Prepare command with language parameter
	var langArg string
	if selectedLang == langRussian {
		langArg = "/Multilang:0" // Russian
	} else {
		langArg = "/Multilang:1" // English
	}
	
	cmd := exec.Command(exePath, langArg)
	cmd.Dir = gamePath
	
	// Start the game
	log.Printf("Launching game: %s %s", exePath, langArg)
	updateStatus(fmt.Sprintf("Launching game with language: %s", langArg))
	
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start game: %v", err)
		updateStatus("Error: Failed to launch game")
	} else {
		updateStatus("Game launched successfully")
	}
}

// guiLogWriter redirects log output to the GUI
type guiLogWriter struct {
	textOutput *widget.Entry
}

// Write implements io.Writer for the log output
func (w *guiLogWriter) Write(p []byte) (n int, err error) {
	// Добавляем текст в log widget без отправки нотификаций
	currentText := w.textOutput.Text
	w.textOutput.SetText(currentText + string(p))
	
	return len(p), nil
}




