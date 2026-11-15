#!/usr/bin/env python3
"""Build a standalone, disposable arm64 SDK lab or empty size control."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import zipfile

parser = argparse.ArgumentParser()
parser.add_argument('--sdk',type=Path,required=True)
parser.add_argument('--work',type=Path,required=True)
source=parser.add_mutually_exclusive_group(required=True)
source.add_argument('--aar',type=Path)
source.add_argument('--sdk-tree',type=Path)
parser.add_argument('--baseline',action='store_true')
args=parser.parse_args()
assert os.environ.get('REMOTE_AGENT_HOME','').startswith('/tmp/')
assert str(args.work).startswith('/tmp/')
root=Path(__file__).resolve().parent
work=args.work/('baseline' if args.baseline else 'full')
work.mkdir(parents=True,exist_ok=True)
for name in ['classes','dex','sdk']:
    p=work/name
    if p.exists():shutil.rmtree(p)
    p.mkdir()
if args.sdk_tree:shutil.copytree(args.sdk_tree,work/'sdk',dirs_exist_ok=True)
else:
    with zipfile.ZipFile(args.aar) as z:z.extractall(work/'sdk')
bt=args.sdk/'build-tools/36.1.0'; android=args.sdk/'platforms/android-36/android.jar'
def run(argv):subprocess.run([str(a) for a in argv],check=True,timeout=120)
sources=[root/'AndroidLab.java',root/'CrossAndroid.java',root/'SDPCheck.java']
if args.baseline:
    source=work/'AndroidLab.java'
    source.write_text('package example.transportlab; public final class AndroidLab extends android.app.Activity {}\n')
    sources=[source]
run(['javac','-source','8','-target','8','-classpath',str(android)+os.pathsep+str(work/'sdk/classes.jar'),'-d',work/'classes',*sources])
classes=list((work/'classes').rglob('*.class'))
if not args.baseline:classes.append(work/'sdk/classes.jar')
run([bt/'d8','--lib',android,'--min-api','26','--output',work/'dex',*classes])
manifest=work/'AndroidManifest.xml'
manifest.write_text('''<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="example.transportlab"><uses-permission android:name="android.permission.INTERNET"/><uses-permission android:name="android.permission.ACCESS_NETWORK_STATE"/><application android:usesCleartextTraffic="true" android:theme="@android:style/Theme.Material.Light.NoActionBar" android:label="SDK Lab" android:debuggable="true"><activity android:name=".AndroidLab" android:exported="true"><intent-filter><action android:name="android.intent.action.MAIN"/><category android:name="android.intent.category.LAUNCHER"/></intent-filter></activity></application></manifest>''')
run([bt/'aapt2','link','-I',android,'--manifest',manifest,'--min-sdk-version','26','--target-sdk-version','35','-o',work/'unsigned.apk'])
with zipfile.ZipFile(work/'unsigned.apk','a') as z:
    z.write(work/'dex/classes.dex','classes.dex',compress_type=zipfile.ZIP_DEFLATED)
    if not args.baseline:
        for f in (work/'sdk/jni/arm64-v8a').glob('*.so'):
            z.write(f,'lib/arm64-v8a/'+f.name,compress_type=zipfile.ZIP_STORED)
run([bt/'zipalign','-f','-P','16','4',work/'unsigned.apk',work/'aligned.apk'])
key=args.work/'lab.keystore'
if not key.exists():run(['keytool','-genkeypair','-noprompt','-keystore',key,'-storepass','android','-keypass','android','-alias','lab','-keyalg','RSA','-dname','CN=TransportLab','-validity','2'])
run([bt/'apksigner','sign','--ks',key,'--ks-pass','pass:android','--key-pass','pass:android','--out',work/'lab.apk',work/'aligned.apk'])
print(('baseline' if args.baseline else 'sdk')+' APK bytes: '+str((work/'lab.apk').stat().st_size))
