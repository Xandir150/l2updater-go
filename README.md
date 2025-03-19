# L2 Game Updater

A Go-based file updating system for L2 game client. The project includes server and client components that provide secure and efficient game file updates.

## Project Structure

- **client-standalone** - GUI client for file updating with Fyne interface
- **server** - Server-side component for generating and serving update manifests
- **standalone** - Utility files for building and packaging the client

## Key Features

### Security
- Filename obfuscation for cloud storage
- AES-256 encryption for archived files
- File integrity verification using SHA-256 hashes
- Secure manifest transmission

### Performance
- Fast file checking using size comparison instead of full hash calculation
- Parallel file verification (5 concurrent checks)
- Parallel downloading and archive unpacking
- Progressive indicators for tracking the update process

### Usability
- Graphical interface with language selection (Russian/English)
- Clear update process information
- Automatic game launch after updating
- Error handling with informative messages

## Technical Features

- Data compression for efficient storage and transfer
- Client-server architecture with scalability
- HTTP-download with multi-threaded support
- Resource management for optimal CPU and memory usage

## Requirements

- Go 1.19 or higher
- Fyne for GUI client
- Docker (for cross-platform builds with fyne-cross)
- Access to storage (Google Cloud Storage or similar)

## Building

The project uses Docker-based cross-compilation with fyne-cross for Windows builds.

### Building the standalone client:
```bash
./build-client-standalone.sh
```

### Building the standalone updater:
```bash
./build-standalone.sh
```

Both scripts will:
- Install fyne-cross if it's not already present
- Build Windows binaries using Docker containers
- Package the application as a ZIP file
- Place build outputs in the release directory

### Building server:
```bash
cd server
go build
```

## Developer Notes

- fyne-cross requires Docker to be installed and running
- The build process is configured for Windows AMD64 architecture
- Binaries are automatically packaged as ZIP files with all necessary assets

## Google Cloud Storage Setup

### Creating a Public Bucket

1. Go to Google Cloud Console: https://console.cloud.google.com/
2. Navigate to Storage > Browser
3. Click "Create Bucket"
   - Choose a globally unique bucket name (e.g., `client00.l2games.net`)
   - Select a location type (Regional recommended for lower latency)
   - Choose Storage class (Standard is recommended for frequently accessed files)
   - Under "Access control", select "Fine-grained"
   - Click "Create"

4. Make the bucket public:
   - Select your new bucket
   - Go to the "Permissions" tab
   - Click "Add" and add a new member with the following details:
     - New members: `allUsers`
     - Role: Storage Object Viewer
   - Click "Save"

### Setting Up Service Account for Uploads

1. Navigate to IAM & Admin > Service Accounts
2. Click "Create Service Account"
   - Enter a service account name (e.g., "l2updater-service")
   - Click "Create and Continue"
   - Grant Storage Admin role
   - Click "Done"

3. Generate a key file:
   - Click on your newly created service account
   - Go to the "Keys" tab
   - Click "Add Key" > "Create new key"
   - Select JSON format
   - Click "Create" to download the key file

4. Store the key file securely in your project directory (not in a public repository!)

### Configuring the Project

#### Server Configuration

In `server/main.go`, look for the following constants and update them:

```go
const (
    bucketName   = "client00.l2games.net" // Change to your bucket name
    encryptionKey = "your-secret-key"     // Change to your secret key
)
```

Set the service account key file path via environment variable before running the server:
```bash
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/your-key-file.json"
```

#### Client Configuration

In `client-standalone/main.go`, update these constants:

```go
const (
    // ...
    encryptionKey     = "your-secret-key"       // Match server encryption key
    defaultBucketName = "client00.l2games.net" // Set to your public bucket name
    // ...
)
```

The client uses HTTP to download files, so it doesn't need authentication credentials for the bucket if it's public.
