# Vercel Proxy

Simple http proxy for Vercel.

### Delpoy

[![Deploy with Vercel](https://vercel.com/button)](https://vercel.com/new/clone?repository-url=https%3A%2F%2Fgithub.com%2FTBXark%2Fvercel-proxy)

### Docker

```bash
docker run -d --name vercel-proxy -p 3000:3000 ghcr.io/tbxark/vercel-proxy:latest
```

Or with docker compose:

```bash
docker compose up -d
```

To customize behavior, mount a JSON config file and pass `--config`:

```bash
docker run -d --name vercel-proxy -p 3000:3000 \
  -v $(pwd)/config.json:/config/config.json \
  ghcr.io/tbxark/vercel-proxy:latest --addr :3000 --config /config/config.json
```

### Usage

```javascript
fetch("https://project-name.vercel.app/https://example.com?param1=value1&param2=value2")
.then((res) => res.text())
.then(console.log.bind(console))
.catch(console.error.bind(console));

```

```bash
curl -L https://project-name.vercel.app/https:/example.com?param1=value1&param2=value2
```

Just add `https://project-name.vercel.app/` before the url you want to proxy.

### License

**vercel-proxy** is released under the MIT license. [See LICENSE](LICENSE) for details.