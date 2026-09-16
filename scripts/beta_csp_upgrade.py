"""Pinned Beta-Agent-only follow-up to the resource-alias deployment."""
import argparse
import copy
import beta_tunnel_upgrade as upgrade

upgrade.OLD = upgrade.BASE / 'bundle-tunnel-1cc79b1/executor'
upgrade.NEW = upgrade.BASE / 'bundle-tunnel-csp/executor'
upgrade.NEW_HASH = 'b29e5e7da9f55a4e5981e4f30ca8c54dcad93ded186b9787b59f540615447cb4'
upgrade.BACKUP = upgrade.BASE / 'deployment-backups/tunnel-agent-csp-61aeb58'
original_planner = upgrade.planned_config

def plan(cfg, plist):
    if cfg.get('oauth_resource_aliases') != [upgrade.ALIAS]:
        raise ValueError('Expected exact installed Tunnel alias')
    clean = copy.deepcopy(cfg)
    del clean['oauth_resource_aliases']
    return original_planner(clean, plist)

def update_stop(text):
    old = "('bundle-tunnel-1cc79b1' if role == 'agent' else 'bundle-ec5f53d')"
    if text.count(old) != 1:
        raise ValueError('Unexpected Beta stop helper')
    return text.replace(old, "('bundle-tunnel-csp' if role == 'agent' else 'bundle-ec5f53d')")

upgrade.planned_config = plan
upgrade.updated_stop = update_stop

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group()
    group.add_argument('--apply', action='store_true')
    group.add_argument('--rollback', action='store_true')
    args = parser.parse_args()
    if args.rollback:
        upgrade.rollback()
    else:
        upgrade.run(args.apply)
