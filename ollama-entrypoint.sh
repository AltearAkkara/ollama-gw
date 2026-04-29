#!/bin/sh
set -e

MODEL="${DEFAULT_MODEL:-gemma3}"

# Start Ollama server in background
ollama serve &
OLLAMA_PID=$!

# Wait until Ollama API is ready
echo "Waiting for Ollama to be ready..."
until ollama list > /dev/null 2>&1; do
  sleep 1
done
echo "Ollama ready."

# Pull model only if not already present
if ! ollama list | grep -q "^${MODEL}"; then
  echo "Pulling model: ${MODEL}"
  ollama pull "${MODEL}"
else
  echo "Model ${MODEL} already present, skipping pull."
fi

# Hand off to Ollama server (wait on it)
wait $OLLAMA_PID
