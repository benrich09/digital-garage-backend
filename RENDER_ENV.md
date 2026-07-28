# Render environment (required for Flutter ↔ Go)

In Render → your service → **Environment**, set at least:

| Key | Example / notes |
|-----|-----------------|
| `SUPABASE_URL` | `https://thenoifpvygqjdxgtehz.supabase.co` |
| `SUPABASE_JWT_SECRET` | Supabase Dashboard → Project Settings → API → JWT Secret |
| `DATABASE_URL` | Pooler connection string (IPv4), not the IPv6-only direct host |
| `SUPABASE_SERVICE_ROLE_KEY` | Key whose JWT payload has `"role":"service_role"` |
| `ENV` | `production` |
| `CORS_ALLOWED_ORIGINS` | Your web origins, or `*` for short tests |

After saving, **Manual Deploy** so the new env is applied.

Verify:

```bash
curl -s https://digital-garage-backend.onrender.com/healthz
```

With a real car-owner access token:

```bash
curl -s -H "Authorization: Bearer ACCESS_TOKEN" \
  https://digital-garage-backend.onrender.com/service-requests/mine
```

- **401** → `SUPABASE_URL` / JWT secret wrong or missing  
- **403 profile not found** → no row in `public.profiles` for that user  
- **403 role not permitted** → profile.role is not `car_owner`  
- **timeout** → free-tier cold start; wait and retry, or upgrade plan  
