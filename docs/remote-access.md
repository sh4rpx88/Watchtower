# Phone app and remote access

Watchtower is an installable web app: on a phone it gets its own home screen icon and opens full screen, with no
browser bar. Phones only offer this over HTTPS, and you'll want to reach the panel when you're away from home, so the
simplest setup is [Tailscale](https://tailscale.com). It's free for personal use, keeps the panel off the public
internet, and gives you HTTPS without setting up certificates.

## 1. Put Gaia on Tailscale

On the machine running Watchtower:

```sh
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up            # opens a login link; sign in with your account
```

In the [Tailscale admin console](https://login.tailscale.com/admin/dns), turn on **MagicDNS** and **HTTPS
Certificates** (both on the DNS page).

Then serve the panel over HTTPS on your tailnet. Port 8443 keeps clear of anything else on the machine that already
uses 443, such as Nginx Proxy Manager or another reverse proxy:

```sh
sudo tailscale serve --bg --https=8443 8080
sudo tailscale serve status    # shows the address
```

Tailscale adds its own network interface (a `100.x.y.z` address) beside your normal one, so it doesn't take over or
move any ports other apps on the machine already use. Your panel is at an address like
`https://gaia.your-tailnet.ts.net:8443`; keep the `https://` and the `:8443`, or you'll reach whatever else answers on
the normal web ports.

If you previously ran `tailscale serve` on another port, clear it first with `sudo tailscale serve reset`. That's your panel from anywhere your devices are signed
in to Tailscale. On your home network, `http://<gaia's IP>:8080` keeps working as before.

## 2. Install it on your phone

1. Install the Tailscale app and sign in with the same account.
2. Open the `https://….ts.net:8443` address.
   - **iPhone (Safari):** tap Share, then **Add to Home Screen**.
   - **Android (Chrome):** tap the menu, then **Install app** (or **Add to Home screen**).
3. Open Watchtower from the new icon and log in.

The app always shows live data. If it can't reach the panel, it shows a page asking you to check your connection or
Tailscale.

## 3. Give someone else access

1. They make their own free Tailscale account and install the app.
2. In the admin console, open **Machines**, click the **…** next to Gaia, choose **Share**, and send them the invite
   link. They can reach Gaia, and none of your other devices.
3. In Watchtower, open **Users** and add them. **Operator** lets them start, stop and restart servers and use the
   console, files and backups; **Viewer** only lets them watch. What they do appears on the Activity page under their
   name.

**Limit them to the panel.** Tailscale's default rules let anyone you share Gaia with reach every port on it, which
includes anything else Gaia runs (such as media apps or download clients). To allow shared users only the Watchtower
address (port 8443, from `tailscale serve` above), replace the policy on the admin console's **Access controls** page with:

```json
{
  "grants": [
    { "src": ["autogroup:member"], "dst": ["*"], "ip": ["*"] },
    { "src": ["autogroup:shared"], "dst": ["*"], "ip": ["8443"] }
  ]
}
```

The first line keeps full access for you and your own devices; the second limits everyone you've shared with to port
8443 on the machines you shared. See Tailscale's [sharing guide](https://tailscale.com/docs/features/sharing) for more.

Playing on the game servers doesn't need Tailscale; players still connect through the game ports as usual.
