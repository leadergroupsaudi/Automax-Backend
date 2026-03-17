# Automax Backend

A comprehensive incident and workflow management system built with Go and modern tooling.

## Technology Stack

- **Language:** Go 1.24.1
- **Framework:** Fiber v2 (Fast HTTP framework)
- **Database:** PostgreSQL 15
- **Caching:** Redis 7
- **Object Storage:** MinIO (S3-compatible)
- **ORM:** GORM
- **Authentication:** JWT v5

## Features

- **Authentication & Authorization**
  - User registration and login with JWT tokens
  - Role-based access control (RBAC)
  - Granular permission system
  - Session management via Redis

- **Incident Management**
  - Support for Incidents, Requests, Complaints, and Queries
  - Workflow state transitions
  - Comments and attachments
  - SLA monitoring
  - Revision history and audit trails

- **Workflow Engine**
  - Dynamic workflow creation
  - States and transitions
  - Transition requirements and actions
  - Workflow duplication and versioning

- **Reporting System**
  - Dynamic report creation
  - Report templates
  - Data export functionality

- **Additional Features**
  - Hierarchical classifications
  - Location and department management
  - Action logging for audit trails
  - File storage with MinIO

## Project Structure

```
Automax-Backend/
├── cmd/server/           # Application entry point
│   └── main.go
├── internal/
│   ├── config/           # Configuration management
│   ├── database/         # Database connections & migrations
│   ├── handlers/         # HTTP request handlers
│   ├── services/         # Business logic
│   ├── repository/       # Data access layer
│   ├── models/           # Data models
│   ├── middleware/       # HTTP middleware
│   └── storage/          # MinIO file storage
├── pkg/utils/            # Utility functions
├── migrations/           # Database migrations
├── Dockerfile
├── docker-compose.yml
└── .env.example
```

## Prerequisites

- Go 1.24+
- PostgreSQL 15+
- Redis 7+
- MinIO (or S3-compatible storage)

## Environment Variables

Create a `.env` file based on `.env.example`:

```env
# Server
SERVER_PORT=8080
SERVER_HOST=0.0.0.0

# Database
DB_HOST=localhost
DB_PORT=5432
DB_USER=automax
DB_PASSWORD=automax123
DB_NAME=automax
DB_SSLMODE=disable

# Redis
REDIS_HOST=localhost
REDIS_PORT=6379
REDIS_PASSWORD=
REDIS_DB=0

# MinIO
MINIO_ENDPOINT=localhost:9000
MINIO_ACCESS_KEY=minioadmin
MINIO_SECRET_KEY=minioadmin
MINIO_USE_SSL=false
MINIO_BUCKET=automax

# JWT
JWT_SECRET=your-super-secret-jwt-key-change-in-production
JWT_EXPIRE_HOUR=24
```
# OTP Configuration
OTP_MAX_SEND_ATTEMPT=6
VERIFY_OTP_MAX_ATTEMPT=3
OTP_DATA_REDIS_EXPIRATION_TIME=2
OTP_COUNTER_EXPIRATION_TIME=2

# Escalation Configuration 
ESCALATION_DAILY_HOUR=15
ESCALATION_DAILY_MINUTE=10
ESCALATION_WEEKLY_HOUR=09
ESCALATION_WEEKLY_MINUTE=00
FRONTEND_BASE_URL=

# notify when < 24 hour left
 READY_TO_CLOSE_PRE_EXPIRY_HOURS=24


## Installation & Running

### Local Development

1. **Clone the repository**
   ```bash
   git clone <repository-url>
   cd Automax-Backend
   ```

2. **Install dependencies**
   ```bash
   go mod download
   ```

3. **Start infrastructure services**
   ```bash
   docker-compose up -d postgres redis minio
   ```

4. **Run the application**
   ```bash
   go run ./cmd/server/main.go
   ```

### Using Docker

1. **Build and run all services**
   ```bash
   docker-compose up --build
   ```

2. **Or build the image separately**
   ```bash
   docker build -t automax-backend .
   docker run -p 8080:8080 --env-file .env automax-backend
   ```

## API Documentation

### Base URL
```
http://localhost:8080/api/v1
```

### Authentication
All protected endpoints require a Bearer token:
```
Authorization: Bearer <token>
```

### Key Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/auth/register` | Register new user |
| POST | `/auth/login` | Login |
| GET | `/users/me` | Current user profile |
| GET/POST | `/incidents` | Incident operations |
| GET/POST | `/requests` | Request operations |
| GET/POST | `/complaints` | Complaint operations |
| GET/POST | `/queries` | Query operations |
| GET/POST | `/admin/users` | User management |
| GET/POST | `/admin/roles` | Role management |
| GET/POST | `/admin/workflows` | Workflow management |
| GET/POST | `/admin/classifications` | Classification management |
| GET/POST | `/admin/departments` | Department management |
| GET/POST | `/admin/locations` | Location management |
| GET/POST | `/admin/reports` | Report management |

## Database Models

- **User** - Users with roles, permissions, departments
- **Role** - User roles with permissions
- **Permission** - Granular permission codes
- **Incident** - Main records (incidents/requests/complaints/queries)
- **Workflow** - State machines for incidents
- **WorkflowState** - States within workflows
- **WorkflowTransition** - Transitions between states
- **Classification** - Hierarchical incident categories
- **Department** - Organizational departments
- **Location** - Geographic locations
- **Report** - Dynamic reports
- **ActionLog** - Audit trail

## Development

### Build
```bash
go build -o automax-backend ./cmd/server
```

### Run Tests
```bash
go test ./...
```

### Lint
```bash
golangci-lint run
```

## Docker Compose Services

| Service | Port | Description |
|---------|------|-------------|
| postgres | 5432 | PostgreSQL database |
| redis | 6379 | Redis cache |
| minio | 9000, 9090 | MinIO object storage |
| backend | 8080 | API server |

## License

Proprietary - All rights reserved
