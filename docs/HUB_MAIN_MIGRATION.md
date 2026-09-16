# Hub mainline migration (in progress)

Local main merge: `923013c`. The merge resolved documentation only; source code
matches the accepted Hub branch. This is not yet a completed production cutover
or a published release. Original credentials and enrolled device IDs are retained.

## Installed new startup services

- Pi5: persistent enabled `executor-hub.service` and
  `executor-hub-device-{broker,desktop,relay}.service`. Replaces the transient
  pilot units. Existing Hub state and device state under
  `/home/jamie/.local/share/executor-hub-pilot` are retained.
- WSL: persistent enabled `executor-hub-device-{broker,desktop,relay}.service`.
  Uses the existing isolated state, not `/var/lib/executor`.
- CTPS: automatic LocalSystem services `ExecutorHubBroker` and
  `ExecutorHubRelay`; duplicate broker/dashboard logon tasks are disabled.
  `ExecutorHubPilot-desktop` remains an interactive-user logon task.
- Mac: `com.executor.hub.broker` and `com.executor.hub.relay` LaunchDaemons,
  plus `com.executor.hub.desktop` user LaunchAgent. Uses the existing Beta state
  and `bundle-main-923013c`, signed with the same developer identity. Native
  build metadata was verified to identify clean merge revision 923013c.
  Superseded Beta broker/dashboard/desktop labels were disabled and unloaded;
  the Beta Agent remains until the management cutover is finished.

Backups are under each pilot root's `main-rollout-backup-923013c`; on Mac they
are under the Executor Beta installation. The one-shot Mac rollout job was
removed after success. Operational scripts remain outside tracked source in
the original checkout's ignored `.worktrees/hub-main-rollout` directory.

## Pending gates

1. Persist the existing OpenAI runtime key in an approved encrypted systemd
   credential on Pi5, then replace the current tmux runtime with an enabled
   Tunnel Client system service. Do not run both with the same Tunnel ID.
2. Verify all four Hub devices after startup-service changes.
3. Disable/unload the old production Executor services and autostart entries;
   they have not yet been stopped by this migration. Preserve binaries and
   state for manual rollback, not automatic fallback.
4. Retain the Pi5 Cloudflare ingress needed for Hub browser OAuth; do not stop
   shared Cloudflare services solely because they also carried an old endpoint.
5. Update ChatGPT connections, run one bounded file write/read per host, and
   verify stopped/disabled old services and enabled new services. An actual
   machine reboot has not been performed.
6. Record the final deployment state and synchronize main as appropriate.

Background services start at boot; desktop control requires a logged-in,
unlocked user desktop. WSL services start when its distribution is started;
Windows boot-time distribution startup still needs configuration/verification.
