#!/usr/bin/env python3
"""Read successful production/main source metadata from Wrangler's JSON output."""
import argparse
import json
import re
import sys


def source(rows):
    for row in rows:
        status = row.get('Status', '')
        succeeded = status == 'just now' or re.fullmatch(r'\d+ (second|minute|hour|day|week|month|year)s? ago', status)
        if row.get('Environment') == 'Production' and row.get('Branch') == 'main' and succeeded:
            commit = row.get('Source', '')
            if not re.fullmatch(r'[0-9a-f]{7}', commit):
                raise ValueError('Production deployment has no verifiable source commit')
            return commit
    raise ValueError('No successful production/main deployment was found')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', action='store_true')
    parser.add_argument('--commit')
    args = parser.parse_args()
    try:
        current = source(json.load(sys.stdin))
        if args.source:
            print(current)
        elif not args.commit or not re.fullmatch(r'[0-9a-f]{40}', args.commit):
            raise ValueError('Expected a full release commit')
        elif not args.commit.startswith(current):
            return 1
        return 0
    except (ValueError, TypeError, KeyError) as error:
        print(f'web deployment: {error}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
