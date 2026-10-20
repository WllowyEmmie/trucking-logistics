# Deploying the demo (free: Supabase + Render + Vercel)

| Piece | Where | Notes |
|---|---|---|
| Database | Supabase (Ireland) | Already set up and migrated |
| API + agent | Render, one free web service (Frankfurt) | `Dockerfile` + `render.yaml`. The agent runs inside the same container; the API serves it at `/agent` |
| Website | Vercel (Hobby, non-commercial) | Forwards `/api` and `/agent` to Render |
| Keep-awake | UptimeRobot or cron-job.org | Render's free service sleeps after 15 min without requests |

The agent's code now lives in `agent/` in this repo. Run it locally from there (`cd agent`, then `uvicorn app.main:app --port 8200`); the old Trucking-agent repo can be archived.

## 1. Keys and passwords

Never commit these or paste them in chat.

- **OpenRouter:** create a new key for this deployment, and delete the old one (it was shared).
- **AGENT_API_KEY:** Render generates it (`generateValue` in `render.yaml`). The API and the agent read the same variable inside the container, so there's nothing to copy.
- **Supabase database password:** the one you reset. It goes only into `DATABASE_URL` on Render.
- **Dispatcher password:** the new one you made with `cmd/adduser`.

## 2. Render (API + agent)

1. Push this repo to GitHub.
2. In Render: **New → Blueprint**, choose this repo. It reads `render.yaml`.
3. Fill in the values it asks for:
   - `DATABASE_URL`: the Supabase **session pooler** string, password encoded, ending `?sslmode=require`.
   - `OPENROUTER_API_KEY`: the new key.
   - `CORS_ORIGINS`: the website's address. You don't have it yet: put `https://example.com` for now and fix it in step 4.
   - `GEOCODER_EMAIL`: a contact email (OpenStreetMap's address search asks for one).
4. Deploy. The first build takes several minutes. When it's live, open `https://<your-service>.onrender.com/health`: it should say ok.

Each start runs the migrations first (`deploy/start.sh`), so a new migration is applied by the next deploy.

## 3. Vercel (website)

1. In Vercel: **Add New → Project**, import Trucking_Logistics_Frontend.
2. Environment variables (Production):
   - `BACKEND_URL` = `https://<your-service>.onrender.com`
   - `AGENT_BACKEND_URL` = `https://<your-service>.onrender.com/agent`
   - `NEXT_PUBLIC_WS_URL` = `wss://<your-service>.onrender.com/ws`
   - `NEXT_PUBLIC_API_MOCKING` = `disabled`
3. Deploy, and note the address (e.g. `https://trucking-logistics.vercel.app`).

`NEXT_PUBLIC_*` values are built into the page: change one, then redeploy.

## 4. Connect them

1. Render → the service → Environment: set `CORS_ORIGINS` to the Vercel address exactly (`https://…vercel.app`, no trailing slash). Save; it redeploys.
2. Without this, the live-updates socket is refused.

## 5. Keep it awake

Make a free HTTP monitor (UptimeRobot or cron-job.org) on `https://<your-service>.onrender.com/health`, every 5 minutes. Render's free hours cover one service running all month.

## 6. Check

1. Open the Vercel address and sign in with your dispatcher account.
2. Settings: company name, time zone, service area (Lagos), depot. Save.
3. The console's live dot should go green (the socket connected with a pass).
4. Ask the agent a question in the console. If it says the agent isn't running, check Render's logs for `supervisor: listening for changes`.
5. On a phone, open the driver app and sign in: the shift starts and GPS asks for permission (needs the https address).

## If something fails

- **Render logs** show the migration output, the agent's lines (`supervisor: …`, `tick: …`) and the API's requests.
- **Out of memory** on Render (512 MB): the agent is the heavy part. Tell me; it can move to its own service.
- **Signed in but every page says not signed in:** the cookie isn't coming back through Vercel. Check `BACKEND_URL` has no trailing slash and the site is on https.
