import SwiftUI

/// A question on a page of its own (Android's QuestionPage).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct QuestionScreen: View {
    let session: String
    let question: String

    var body: some View {
        ScreenPlaceholder("提问", detail: [session, question])
            .navBar("提问")
    }
}
