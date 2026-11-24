import RACore

/// Exit-confirmation wording derived from security state facts.
enum AuthorityAccess {
    static func exitAdvice(authority: Bool, unresolved: Bool) -> String {
        if unresolved { return L10n.securityIosASecurityOperationIsUnfinishedRetryOrVerifyIts() }
        if authority { return L10n.securityThisDeviceIsTheOnlyManagingClientTransferManagement() }
        return L10n.securityExitingDeletesLocalKeysYouWillNeedToPair()
    }
}
