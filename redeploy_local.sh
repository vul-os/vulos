#!/bin/sh
set -e

NAME="vulos"
PORT="${PORT:-8080}"
SHM="1g"
VOLUME="vulos-data"

if [ "$1" = "--quick" ]; then
  echo "Quick rebuild — backend + frontend only"

  echo "Building Go backend..."
  cd backend
  CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ../vulos-server ./cmd/server
  cd ..

  echo "Building frontend..."
  npm run build

  echo "Copying into container..."
  docker cp vulos-server "$NAME":/usr/local/bin/vulos-server
  docker cp dist/. "$NAME":/opt/vulos/webroot/

  echo "Restarting container..."
  docker restart "$NAME"

  rm -f vulos-server

  sleep 3
  if docker ps --filter "name=$NAME" --format '{{.Status}}' | grep -q "Up"; then
    echo "Running at http://localhost:$PORT"
  else
    echo "Failed. Logs:"
    docker logs "$NAME" --tail 20
    exit 1
  fi
  exit 0
fi

echo "Stopping existing container..."
docker rm -f "$NAME" 2>/dev/null || true

echo "Building image..."
docker build -t "$NAME" .

echo "Starting container..."
docker run -d \
  --name "$NAME" \
  -p "$PORT:8080" \
  --shm-size="$SHM" \
  -v "$VOLUME:/root/.vulos" \
  "$NAME"

echo "Waiting for startup..."
sleep 3

if docker ps --filter "name=$NAME" --format '{{.Status}}' | grep -q "Up"; then
  echo "Running at http://localhost:$PORT"
else
  echo "Failed to start. Logs:"
  docker logs "$NAME" --tail 20
  exit 1
fi
