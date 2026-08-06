# LAN Port Exposure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Open WebUI service reachable on TCP port 8080 from the host and trusted devices on the home LAN.

**Architecture:** Docker Compose currently binds the `webui` container’s port 8080 only to the IPv4 loopback interface. Replace that host binding with `0.0.0.0` so Docker accepts connections on every IPv4 interface while preserving the container port and the proxy’s internal `proxy:8080` connection. Update the run instructions to distinguish local and LAN URLs and document the exposure boundary.

**Tech Stack:** Docker Compose, Docker port publishing, Markdown.

---

## File structure

- Modify: `docker-compose.yml` — defines the host-to-container port binding for the `webui` service.
- Modify: `README.md` — tells operators how to connect from the LAN and what exposure changes.
- No Go source or Go test files change: the application remains behind the same container port and proxy URL.

### Task 1: Publish Open WebUI on all IPv4 interfaces

**Files:**
- Modify: `docker-compose.yml:23-37`

- [ ] **Step 1: Verify the current configuration exposes only loopback**

Run:

```bash
docker compose config
```

Expected: the rendered `webui` service lists a port mapping with host IP `127.0.0.1`, host port `8080`, and container port `8080`.

- [ ] **Step 2: Replace the `webui` host-port binding**

Change the `ports` entry under `services.webui` from:

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

to:

```yaml
ports:
  - "0.0.0.0:8080:8080"
```

Keep the container port (`8080`), `OPENAI_API_BASE_URL` (`http://proxy:8080/v1`), volumes, and restart policy unchanged.

- [ ] **Step 3: Verify the rendered Compose configuration**

Run:

```bash
docker compose config
```

Expected: the rendered `webui` service has a port mapping with host IP `0.0.0.0`, host port `8080`, and container port `8080`; the command exits successfully.

- [ ] **Step 4: Commit the port-publication change**

```bash
git add docker-compose.yml
git commit -m "fix: expose web UI on LAN"
```

### Task 2: Document LAN access and exposure boundary

**Files:**
- Modify: `README.md:51-59`

- [ ] **Step 1: Replace the access instruction**

Replace the current access sentence with the following content:

```markdown
4. **Access Open WebUI**:
   * On the host, open `http://localhost:8080`.
   * From a device on the home network, open `http://<host-LAN-IP>:8080`, replacing `<host-LAN-IP>` with the host’s private IPv4 address (for example, `192.168.1.10`).
   * The service listens on all IPv4 interfaces, including VPN interfaces. It is not internet-accessible unless the router forwards port 8080; restrict TCP/8080 to trusted networks with the host firewall when one is enabled.
```

- [ ] **Step 2: Review the documented addresses against the Compose binding**

Confirm the text names both `localhost:8080` and `<host-LAN-IP>:8080`, and that its all-interface warning matches the `0.0.0.0:8080:8080` mapping exactly.

- [ ] **Step 3: Re-validate Compose after the documentation-only change**

Run:

```bash
docker compose config
```

Expected: successful exit and the same `0.0.0.0:8080:8080` rendered port mapping verified in Task 1.

- [ ] **Step 4: Commit the documentation change**

```bash
git add README.md
git commit -m "docs: explain LAN access to web UI"
```
