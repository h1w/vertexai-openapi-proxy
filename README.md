# Vertex AI OpenAI Proxy

## Overview

This project provides a proxy server that translates OpenAI API requests to Google Cloud Vertex AI. It allows applications designed to work with the OpenAI API (like Open WebUI) to use Google's Vertex AI models (e.g., Gemini) without significant modification.

The proxy handles:
- Authentication with Google Cloud using Application Default Credentials (ADC).
- Caching of authentication tokens.
- Discovering the live Google publisher catalog through `/v1/models`.
- Proxying supported OpenAI chat completion and native Vertex AI inference requests.

It is designed to be run as a Docker container, typically orchestrated with `docker-compose` alongside an application like Open WebUI.

## Prerequisites

1.  **Google Cloud Project**: You need an active Google Cloud Project with the Vertex AI API enabled.
2.  **Application Default Credentials (ADC)**:
    *   Ensure you have authenticated with Google Cloud CLI: `gcloud auth application-default login`
    *   The proxy relies on the ADC file (typically found at `~/.config/gcloud/application_default_credentials.json` on Linux/macOS) to authenticate with Google Cloud. This file needs to be mounted into the proxy container.
3.  **Environment Variables**:
    *   `VERTEXAI_PROJECT`: Your Google Cloud Project ID.
    *   `VERTEXAI_LOCATION`: The Vertex AI endpoint location. Use `global` to discover the global Google publisher-model catalog.
    *   `VERTEXAI_PROXY_API_KEY`: A required long, random secret used to authenticate requests to the proxy.
4.  **Docker and Docker Compose**: Required to build and run the service.

## How to Run

The project includes a `docker-compose.yml` file for easy setup with Open WebUI.

1.  **Create `.env` from the template**:
    ```bash
    cp .example.env .env
    ```
    Set `VERTEXAI_PROJECT` to your Google Cloud project. The template uses `VERTEXAI_LOCATION=global`, which discovers the global Google publisher-model catalog. Generate a unique `VERTEXAI_PROXY_API_KEY`; Docker Compose passes it to both the proxy and Open WebUI.

2.  **Provide ADC credentials**:
    The included `docker-compose.yml` mounts `~/Documents/ADC.json` into the proxy container. After `gcloud auth application-default login`, either copy the default ADC file:
    ```bash
    mkdir -p ~/Documents
    cp ~/.config/gcloud/application_default_credentials.json ~/Documents/ADC.json
    ```
    or change the `proxy` volume mount in `docker-compose.yml` to your existing ADC path.

3.  **Start the Services**:
    ```bash
    docker compose up -d
    ```
    This will build the proxy image (if not already built) and start both the `proxy` and `webui` services.

4.  **Access Open WebUI**:
    On the host, open `http://localhost:8080`. From a device on the home network, open `http://<host-LAN-IP>:8080`, replacing `<host-LAN-IP>` with the host's private IPv4 address (for example, `192.168.1.10`). The service listens on all IPv4 interfaces, including VPN interfaces; restrict TCP/8080 to trusted networks with the host firewall when one is enabled.

## How to Test

The Go application includes unit tests.

1.  **Ensure Go is installed.**
2.  **Navigate to the project directory.**
3.  **Run tests:**
    ```bash
    go test ./...
    ```

## Configuration

### Proxy Service (`main.go`)

The proxy service is configured via environment variables:

*   `VERTEXAI_PROJECT`: (Required) Your Google Cloud Project ID.
*   `VERTEXAI_LOCATION`: (Required) `global` for the global Vertex publisher-model catalog, or a specific supported Vertex region.
*   `VERTEXAI_PROXY_API_KEY`: (Required) A long, random secret that clients must send as a Bearer credential to authenticate with the proxy.
*   `GOOGLE_APPLICATION_CREDENTIALS`: (Set within `docker-compose.yml`) Points to the path of the mounted ADC JSON file inside the container (e.g., `/app/gcp_adc.json`).

*   `LOG_LEVEL`: (Optional) Sets the logging level.
    *   Supported values: `debug`, `info`, `warn`, `error`.
    *   Defaults to `info` if not set or invalid.
*   `LOG_FORMAT`: (Optional) Sets the log output format.
    *   Supported values: `text` (human-readable), `json` (structured).
    *   Defaults to `text` if not set or invalid.

*   `PORT`: (Optional) Sets the listening port for the proxy server.
    *   Defaults to `8080` if not specified.


### Open WebUI Service (`docker-compose.yml`)

The `webui` service in `docker-compose.yml` is pre-configured to use the proxy and receives `VERTEXAI_PROXY_API_KEY` unchanged as its API key:

*   `OPENAI_API_BASE_URL: http://proxy:8080/v1`
*   `OPENAI_API_KEY: ${VERTEXAI_PROXY_API_KEY:?Set VERTEXAI_PROXY_API_KEY in .env}`

## Model discovery and API surfaces

Both API surfaces require `Authorization: Bearer $VERTEXAI_PROXY_API_KEY`. Docker Compose requires this one secret and passes it to the proxy as `VERTEXAI_PROXY_API_KEY` and to Open WebUI as `OPENAI_API_KEY`.

### OpenAI-compatible surface

* `GET /v1/models` returns the live Google publisher catalog as an OpenAI `ModelList`; there is no static model allowlist.
* `POST /v1/chat/completions` remains OpenAI-compatible for models that support Vertex Chat Completions. Open WebUI can display other catalog models, but it cannot call non-chat capabilities through this endpoint.

### Native Vertex AI surface

* `GET http://localhost:8081/vertex/v1/models` returns the catalog. LAN clients can use `http://<host-LAN-IP>:8081`.
* Send native inference requests to exactly `/vertex/v1/models/google/{model}:{action}`. For example:

  ```bash
  curl --request POST \
    --url http://localhost:8081/vertex/v1/models/google/gemini-2.5-flash:generateContent \
    --header "Authorization: Bearer $VERTEXAI_PROXY_API_KEY" \
    --header "Content-Type: application/json" \
    --data '{
      "contents": [
        {
          "role": "user",
          "parts": [{"text": "Hello"}]
        }
      ]
    }'
  ```

* The native route supports only these inference actions: `generateContent`, `streamGenerateContent`, `embedContent`, `predict`, `rawPredict`, `streamRawPredict`, `serverStreamingPredict`, `predictLongRunning`, and `fetchPredictOperation`. Other Vertex management paths are intentionally blocked.

Catalog presence does not guarantee free-trial, regional, quota, or allowlist access. Vertex AI returns the authoritative inference error.

## Logging

The proxy service logs information about incoming requests, token fetching, and upstream communication to standard output.

### Viewing Logs

You can view these logs using:
```bash
docker compose logs proxy
```
Or, if running the Go application directly:
```bash
go run main.go
```

### Configuring Log Output

You can control the verbosity and format of the logs using environment variables:

*   **`LOG_LEVEL`**:
    *   Determines the minimum level of logs to display.
    *   Supported values:
        *   `debug`: Detailed information, useful for troubleshooting.
        *   `info`: Standard operational information (default).
        *   `warn`: Warnings about potential issues.
        *   `error`: Error messages.
    *   Example: To set the log level to debug:
        ```bash
        LOG_LEVEL=debug go run main.go
        ```
        Or in `docker-compose.yml` or your `.env` file:
        ```env
        LOG_LEVEL=debug
        ```

*   **`LOG_FORMAT`**:
    *   Determines the output format of the logs.
    *   Supported values:
        *   `text`: Human-readable, plain text format (default).
        *   `json`: Structured JSON format, suitable for log management systems.
    *   Example: To set the log format to JSON:
        ```bash
        LOG_FORMAT=json go run main.go
        ```
        Or in `docker-compose.yml` or your `.env` file:
        ```env
        LOG_FORMAT=json
        ```

**Example combining both:**
To run with debug level and JSON format:
```bash
LOG_LEVEL=debug LOG_FORMAT=json go run main.go
```
Or in your `.env` file:
```env
LOG_LEVEL=debug
LOG_FORMAT=json
```

## Troubleshooting

*   **Authentication Errors**:
    *   Ensure your ADC file is correctly mounted and `GOOGLE_APPLICATION_CREDENTIALS` inside the container points to it.
    *   Verify the Vertex AI API is enabled in your GCP project.
    *   Check that the service account associated with your ADC (or your user credentials) has the "Vertex AI User" role or equivalent permissions.
*   **`VERTEXAI_PROXY_API_KEY`**: Open WebUI sends this required shared secret unchanged as its API key; the proxy verifies it as the Bearer credential.
*   **Model Access or Capability Errors**: The catalog is discovered live, but presence does not guarantee access in your project, region, quota, free trial, or allowlist. Use `/v1/chat/completions` only with models that support Vertex Chat Completions; use the native route for other supported inference capabilities. Vertex AI returns the authoritative inference error.
*   **CLI tools fail after the first tool call**: Some OpenAI-compatible clients omit Vertex's opaque Gemini thought signature when they return a tool result. The proxy restores the signature by tool-call ID during its short retention window. Use a tool-capable chat model such as `google/gemini-2.5-flash`; TTS and image-generation models do not become tool-capable through the proxy.
