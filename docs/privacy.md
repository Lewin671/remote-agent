# Privacy policy / 隐私政策

Effective: 6 November 2026. Maintainer: **Lewin671 / remote-agent**. Privacy and support contact: [project support](https://github.com/Lewin671/remote-agent/issues). Do not post private keys, recovery codes or private conversations in a public issue.

This policy explains the native remote-agent iOS app and the public relay operated by its maintainer. If you choose a self-hosted relay, its operator controls that server and is responsible for its own infrastructure and privacy practices. The app lets you choose the relay before creating or joining an account. Technical details are in [the reference](reference.md#security-model).

## What is processed

- **On your devices:** the app keeps account and device keys in the iOS Keychain and stores encrypted caches and pending attachments in its container. Local preferences include appearance and read state. You do not need to provide a name, email address or phone number to create an account.
- **On the relay:** random account and device identifiers, public keys, signed device-membership and recovery records, device roles, connection/activity timestamps, session routing identifiers, sequence numbers, expiry times and storage sizes are processed to authenticate devices, route traffic, synchronize records, enforce quotas and prevent abuse. These identifiers and activity records are associated with the relevant account. Network addresses are necessarily processed to establish connections and enforce rate limits. Hosting and network providers also process connection metadata as needed to deliver the service.
- **Your content:** conversation text, project names and paths, commands, images, files and private device names are encrypted on your devices before being sent to the relay. The relay stores and forwards ciphertext; it does not receive the content-decryption keys. Devices you admit to your account can decrypt content according to their permissions. Encryption does not hide routing metadata or traffic timing and sizes.
- **Camera and photos:** camera access is used only when you choose to scan a pairing QR code. The system photo/file pickers provide only the items you choose. Saving an image to Photos requires the permission requested for that action. Attachments you send are delivered to your selected computer.
- **Support:** if you choose to open a GitHub issue, the maintainer and GitHub receive what you send. Do not send recovery codes, private keys or unredacted private conversations. Public GitHub issues are visible to everyone.

The iOS app does not include advertising, advertising identifiers, third-party analytics or cross-app tracking. It currently does not register an iOS push-notification target. We do not sell your data or use it for targeted advertising. The app does not send your conversations directly to an AI service: the CLI on your own computer does so under the account and provider settings you chose. Those providers' policies apply to their processing. Sharing or exporting through another app is also your choice and subject to that destination's policy.

## Retention and deletion

Encrypted conversation records and account metadata remain on the relay until removed through the product or until the account is deleted. Image display-copy bytes normally expire after 30 days; expiry can leave routing/expiry records until account deletion. Temporary pairing offers expire after a few minutes. Your computer's original CLI history and project files remain on that computer.

In the iOS app, open **Settings → Delete entire account…** on the account's current management device. Confirming deletion removes the account, device registrations, stored encrypted conversation/image/file records, settings and notification targets from that relay, and revokes access for its paired devices. A successful response is followed by clearing the initiating phone's keys and caches. Minimal device-revocation markers (key-derived device identifiers and revocation metadata) remain solely to prevent previously revoked identities from registering again. They contain no conversation content or content-decryption keys.

Only the current management device can delete an account. If you lost it, first recover management using your recovery code; the recovery flow explains any security waiting period. **Exit and delete local keys** clears only this phone and does not delete the relay account. Account deletion cannot erase offline copies already held on your other devices, files exported to other apps, or the CLI records and projects on your computers. Clear those copies on those devices if required. If a network interruption makes deletion's result uncertain, the app keeps your local identity so you can check rather than claiming that deletion succeeded.

You can revoke camera/photo permissions in iOS Settings, leave an account, remove paired devices from its management device, or request privacy assistance at the contact above. The relay may be hosted outside your country; self-hosting lets you choose the operator and location. Changes to this policy will be published here with an updated effective date.

---

## 中文说明

生效日期：2026 年 11 月 6 日。维护者：**Lewin671 / remote-agent**。隐私和支持联系入口：[项目支持](https://github.com/Lewin671/remote-agent/issues)。请勿在公开 issue 中提交私钥、恢复码或私人对话。

本政策适用于原生 iOS App 和维护者运营的公共中继。你也可以在建号或加入账户前选择自己的中继；自部署服务器的运营者负责其基础设施和隐私处理。

- 密钥保存在本机钥匙串，缓存和待发附件在 App 容器中加密保存。外观、已读状态等偏好保存在本机。建号不要求姓名、邮箱或手机号。
- 中继处理账户及设备标识、公钥、签名成员链与恢复记录、设备角色、连接及活动时间、会话路由标识、序号、大小和过期时间，用于鉴权、转发、同步、限额和防滥用。这些元数据关联到相应账户。建立连接和限流会处理网络地址；基础设施提供商也会为提供服务处理连接元数据。
- 对话、项目路径、命令、图片、文件和私人设备名称在离开设备前加密。中继只保存和转发密文，没有内容解密密钥。你授权的设备可按权限解密；加密不隐藏路由元数据、传输时间和大小。
- 相机仅用于你主动选择的配对扫码；系统图片和文件选择器只向 App 提供你选择的项目。保存图片到相册时才请求相应权限。发送的附件会交给你选定的电脑。
- iOS App 没有广告、广告标识、第三方统计或跨 App 跟踪，目前不注册 iOS 推送目标。我们不出售数据，也不用于定向广告。向 AI 服务提交内容的是你电脑上的 CLI，相应账号和提供商政策由你选择。主动在 GitHub 提问或通过其他 App 分享内容时，接收方会收到你提交的信息；公开 issue 任何人都能看到，请勿提交恢复码、私钥或未遮挡的私人对话。

加密会话和账户元数据保存到你主动清理或删除账户为止。图片显示副本通常 30 天后清除字节，路由和过期记录可保留至删号。配对邀请仅短期有效。

在当前管理端打开 **设置 → 删除整个账户…**，确认后删除所选中继上的账户、设备登记、加密会话、图片与文件记录、账户设置和通知目标，并使已配对设备失去访问权限。成功后清理发起删除的手机密钥和缓存。为防止旧设备身份重新注册，仍保留最小的设备撤销标记（由公钥派生的设备标识及撤销元数据），不含对话或内容密钥。管理端丢失时，请先用恢复码找回管理权，恢复页面会说明安全等待期。

**退出并删除本机密钥**只清理这台手机，不删除中继账户。删号不会远程抹除其他设备已下载的离线副本、已导出的文件，也不会删除电脑上的原始 CLI 记录和项目；如有需要，请在对应设备清理。请求后断网导致结果不明时，App 会保留本机身份供你核对，不会把未知结果显示为成功。

你可在 iOS 设置撤销相机和相册权限，通过管理端移除设备，或通过上述项目支持入口提出隐私问题。中继可能位于你的国家或地区以外；自部署时由你选择运营者与位置。政策变更会在本页公布并更新生效日期。
