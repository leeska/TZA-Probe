#!/usr/bin/env python3
"""Explicitly refresh offline targets from the source's literal IP addresses.
No DNS resolution or runtime downloads. Valid IP syntax does not prove reachability.
"""
import argparse
import csv
import datetime
import io
import ipaddress
import json
import pathlib
import urllib.request

SOURCE = 'https://tcpquality.ibsgss.uk/getNodes?format=tsv&scope=cdn'
DEST = pathlib.Path(__file__).resolve().parents[1] / 'internal/catalog/targets.json'

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=pathlib.Path, help='Use an explicitly downloaded TSV')
    args = parser.parse_args()
    if args.input:
        with args.input.open('rb') as source:
            data = source.read(1_000_001)
    else:
        request = urllib.request.Request(SOURCE, headers={'User-Agent': 'TZA-Probe-Catalog/2'})
        with urllib.request.urlopen(request, timeout=30) as response:
            data = response.read(1_000_001)
    if len(data) > 1_000_000:
        raise ValueError('unexpected catalogue size')
    rows = [r for r in csv.DictReader(io.StringIO(data.decode()), delimiter='\t')
            if r['type'] == 'cdn' and r['family'] in ('4', '6')]
    if not 100 <= len(rows) <= 300:
        raise ValueError('unexpected catalogue row count; inspect source manually')
    targets = []
    seen = set()
    for row in rows:
        ip = ipaddress.ip_address(row['ip'])
        if ip.version != int(row['family']) or not ip.is_global:
            raise ValueError('source contains invalid family or non-public IP; retaining existing catalogue')
        port = int(row['port'])
        if not 1 <= port <= 65535:
            raise ValueError('source contains invalid port')
        key = (row['province'], row['isp'], row['family'])
        if key in seen:
            raise ValueError('duplicate region/carrier/family in source')
        seen.add(key)
        targets.append(dict(id=row['target'].split('.')[0], family='ipv'+row['family'],
                            region=row['province'], carrier=row['isp'],
                            host=str(ip), ip=str(ip), port=port))
    value = dict(source=SOURCE, snapshot_date=datetime.datetime.now(datetime.timezone.utc).date().isoformat(),
                 verification='Source-provided literal public IPs with validated address family; reachability and ASN are determined by each Agent.',
                 targets=targets)
    temp = DEST.with_suffix('.tmp')
    temp.write_text(json.dumps(value, ensure_ascii=False, indent=2)+'\n')
    temp.replace(DEST)
    print(f'Refreshed {len(targets)} explicit IP targets; no DNS aliases or synthetic addresses')

if __name__ == '__main__':
    main()
