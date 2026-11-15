#!/usr/bin/env python3
"""Build unsigned standalone iOS bundles; never install on a device."""
import argparse
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import zipfile

p=argparse.ArgumentParser();p.add_argument('--framework',type=Path,required=True);p.add_argument('--work',type=Path,required=True);p.add_argument('--baseline',action='store_true');p.add_argument('--simulator',action='store_true');args=p.parse_args()
assert os.environ.get('REMOTE_AGENT_HOME','').startswith('/tmp/') and str(args.work).startswith('/tmp/')
root=Path(__file__).resolve().parent
name=('simulator' if args.simulator else 'device')+('-baseline' if args.baseline else '-sdk')
work=args.work/name;app=work/'Payload/SDKLab.app';app.mkdir(parents=True,exist_ok=True)
sdk='iphonesimulator' if args.simulator else 'iphoneos'
sdkpath=subprocess.check_output(['xcrun','--sdk',sdk,'--show-sdk-path'],text=True).strip()
target='arm64-apple-ios16.0'+('-simulator' if args.simulator else '')
sources=[root/'SDPCheck.swift',root/'IOSLab.swift'];extra=[]
if args.baseline:
    source=work/'main.swift'
    source.write_text('''import UIKit
final class Scene: UIResponder, UIWindowSceneDelegate {
var window: UIWindow?
func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options: UIScene.ConnectionOptions) {
if let s=scene as? UIWindowScene { window=UIWindow(windowScene:s);window?.rootViewController=UIViewController();window?.makeKeyAndVisible() }
}}
final class App: UIResponder, UIApplicationDelegate {
func application(_ app: UIApplication, configurationForConnecting session: UISceneSession, options: UIScene.ConnectionOptions)->UISceneConfiguration {
let c=UISceneConfiguration(name:"Lab",sessionRole:session.role);c.delegateClass=Scene.self;return c
}}
UIApplicationMain(CommandLine.argc,CommandLine.unsafeArgv,nil,NSStringFromClass(App.self))
''');sources=[source]
else:
    slice=args.framework/('ios-x86_64_arm64-simulator' if args.simulator else 'ios-arm64')
    (app/'Frameworks').mkdir(exist_ok=True)
    shutil.copytree(slice/'WebRTC.framework',app/'Frameworks/WebRTC.framework',dirs_exist_ok=True)
    extra=['-F',str(slice),'-framework','WebRTC','-Xlinker','-rpath','-Xlinker','@executable_path/Frameworks']
subprocess.run(['xcrun','swiftc','-swift-version','5','-O','-sdk',sdkpath,'-target',target,'-module-cache-path',str(args.work/'cache'),'-framework','UIKit',*extra,*map(str,sources),'-o',str(app/'Lab')],check=True,timeout=120)
info={'CFBundleIdentifier':'example.transportlab','CFBundleName':'TransportLab','CFBundleExecutable':'Lab','CFBundlePackageType':'APPL','CFBundleVersion':'1','CFBundleShortVersionString':'1.0','MinimumOSVersion':'16.0','LSRequiresIPhoneOS':True,'UIDeviceFamily':[1,2],'UILaunchScreen':{},'UIApplicationSceneManifest':{'UIApplicationSupportsMultipleScenes':False}}
(app/'Info.plist').write_bytes(plistlib.dumps(info))
ipa=work/'unsigned.ipa'
with zipfile.ZipFile(ipa,'w',compression=zipfile.ZIP_DEFLATED,compresslevel=9) as z:
    for f in app.rglob('*'):
        if f.is_file():z.write(f,f.relative_to(work))
print(name,{'app_bytes':sum(f.stat().st_size for f in app.rglob('*') if f.is_file()),'unsigned_ipa_bytes':ipa.stat().st_size})
