# Cache Project

A Go project initialized with Cursor.

## Getting Started

### Prerequisites

- Go 1.21 or later
- Git (optional)

### Installation

1. Clone the repository (or navigate to the project directory)
2. Install dependencies:
   ```bash
   go mod download
   ```

### Running the Project

```bash
go run main.go
```

### Building

```bash
go build -o cache main.go
```

### Testing

```bash
go test ./...
```

### Code Quality

Run formatting and linting:

```bash
# Format code
go fmt ./...

# Run linter
golint ./...

# Run vet
go vet ./...
```

## Project Structure

```
.
├── .cursor/
│   └── rules/          # Cursor rules for AI guidance
│       ├── project-standards.mdc
│       └── go-standards.mdc
├── .gitignore          # Git ignore patterns
├── go.mod              # Go module definition
├── main.go             # Main application entry point
└── README.md           # Project documentation
```

## Cursor Configuration

This project includes Cursor rules in `.cursor/rules/` that provide context and standards for AI-assisted development:

- **project-standards.mdc**: Core project standards (always applies)
- **go-standards.mdc**: Go-specific coding standards (applies when working with `.go` files)

## Development

Add your project-specific setup instructions here.
