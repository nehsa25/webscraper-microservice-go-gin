# Use a Golang image
FROM golang:latest
WORKDIR /app

# Copy go modules to the working directory
COPY go.mod go.sum ./

# Download any dependencies
RUN go mod download

# Copy the rest of the application code
COPY . .

# Set release mode
ENV GIN_MODE=release

# Build the Go application
RUN go build -o scraper .

# Expose port 8080
EXPOSE 8081

# Command to run when the container starts
CMD ["./scraper"]