# Run ProofPulse and Bifrost with Docker (maintainer setup)

This local Docker setup runs the ProofPulse Go service, Bifrost, and the matching ProofPulse Bifrost plugin together. Bifrost listens only inside the container; port 8000 is the only published port. The Bifrost configuration database is kept in a named Docker volume.

> **This is the maintainer self-hosted path.** It requires model-provider, Bifrost, and Tavily credentials in a private `.env.docker` file. Judges should use the keyless Terminal CLI with the team-hosted ProofPulse API once that service URL is published; they should not create or supply provider keys.

## First run

1. Create the private Docker environment file and open it:

   ```bash
   cd ~/proofpulse
   cp .env.docker.example .env.docker
   open -e .env.docker
   ```

2. Replace the three placeholder secrets in `.env.docker`:
   - `OPENROUTER_API_KEY`: your OpenRouter provider key.
   - `BIFROST_API_KEY`: choose a long private value; this is the virtual key ProofPulse sends to Bifrost.
   - `TAVILY_API_KEY`: your Tavily key.

   Keep `BIFROST_MODEL` as shown unless you deliberately select a different model. Save the file. Do not share it or commit it.

3. Build and start the stack in the background:

   ```bash
   docker compose up -d --build
   ```

The first build downloads Bifrost and its dependencies, compiles its web UI, then compiles the Linux Bifrost binary and ProofPulse plugin with the same Go version. It can take several minutes. Docker Desktop must be running; the container runs in the background after startup.

## Check it

In a second Terminal tab:

```bash
curl -sS http://127.0.0.1:8000/health
```

Then ask ProofPulse to generate an answer and check its time-sensitive claims:

```bash
curl -sS -X POST http://127.0.0.1:8000/api/verify -H 'Content-Type: application/json' -d '{"question":"What is the current stable Go release?"}'
```

## Stop and restart

Stop the container with:

```bash
cd ~/proofpulse && docker compose stop
```

Start it again later with `docker compose start`. Inspect logs with `docker compose logs -f`; remove the container with `docker compose down` (the named data volume is retained unless you add `-v`).

This runs on your computer. It is a repeatable Docker deployment, but it does not make the public Manus website or the keyless judge API independent of your computer; a stable hosted service is a separate deployment step.
