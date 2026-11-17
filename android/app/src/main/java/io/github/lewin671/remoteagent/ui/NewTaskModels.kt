package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.LocalIndication
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.*
import androidx.compose.ui.zIndex
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.proto.Agent

private const val PANEL_COLLAPSED_MODELS = 5

/** The CLI or the model trigger in the new task's box: no fill, 15 ink2, a small down arrow, 40 tall. */
@Composable
internal fun BoxTool(
    onClick: () -> Unit,
    label: String,
    modifier: Modifier = Modifier,
    active: Boolean = false,
    content: @Composable RowScope.() -> Unit,
) {
    Row(
        modifier.heightIn(min = 40.dp).clip(Radii.Pill)
            .then(if (active) Modifier.background(pal.fill).semantics { selected = true } else Modifier)
            .clickable(
                interactionSource = remember { MutableInteractionSource() },
                indication = if (active) null else LocalIndication.current,
                role = Role.Button,
                onClick = onClick,
            )
            .semantics(mergeDescendants = true) { contentDescription = label }.padding(horizontal = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        content()
        Arrow(pal.ink3, T.s12, down = true)
    }
}

/** Only the model shrinks: the CLI prefix and the effective effort remain readable. */
@Composable
internal fun NewTaskModelLabel(
    model: String,
    effort: String = "",
    prefix: String = "",
    modifier: Modifier = Modifier,
) {
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        if (prefix.isNotEmpty()) Text(prefix, style = T.s15, color = pal.ink2, maxLines = 1)
        Text(
            model,
            style = T.s15,
            color = pal.ink2,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f, fill = false).testTag("new-model-name"),
        )
        if (effort.isNotEmpty()) {
            Text(
                " · " + effort,
                style = T.s15,
                color = pal.ink2,
                maxLines = 1,
                modifier = Modifier.testTag("new-model-effort"),
            )
        }
    }
}

/**
 * Measure the actual chip contents before giving space to the CLI name. The model
 * gets all remaining width after collapsing the CLI, so its effort stays whole and
 * even an ellipsized model retains a useful prefix on narrow phone tool rows.
 */
@Composable
internal fun NewTaskTools(
    view: NewSessionViewModel,
    modelOpen: Boolean,
    onAgent: () -> Unit,
    onModel: () -> Unit,
    modifier: Modifier = Modifier,
    agentModifier: Modifier = Modifier,
    modelModifier: Modifier = Modifier,
) {
    val agent = view.config.agent.takeIf {
        view.availableAgentsCount > 1 && it != Agent.AGENT_UNSPECIFIED && it != Agent.UNRECOGNIZED
    }
    val prefix = if (view.availableAgentsCount <= 1 && view.agentName.isNotEmpty()) "${view.agentName} · " else ""
    val modelName = uiText(view.modelLabel).ifEmpty { uiText(UiMessages.newSessionDefaultModel()) }
    NewTaskToolsRow(
        agent = agent,
        agentTitle = view.agentName,
        model = modelName,
        effort = view.effortLabel,
        prefix = prefix,
        modelOpen = modelOpen,
        onAgent = onAgent,
        onModel = onModel,
        modifier = modifier,
        agentModifier = agentModifier,
        modelModifier = modelModifier,
    )
}

@Composable
internal fun NewTaskToolsRow(
    agent: Agent?,
    agentTitle: String = "",
    model: String,
    effort: String = "",
    prefix: String = "",
    modelOpen: Boolean,
    onAgent: () -> Unit,
    onModel: () -> Unit,
    modifier: Modifier = Modifier,
    agentModifier: Modifier = Modifier,
    modelModifier: Modifier = Modifier,
) {
    val copy = uiText()
    SubcomposeLayout(modifier) { constraints ->
        val loose = constraints.copy(minWidth = 0, minHeight = 0)
        val gap = if (agent == null) 0 else 2.dp.roundToPx()
        val modelMeasurable = subcompose("model") {
            val desc = uiText(
                if (effort.isEmpty()) UiMessages.modelsSummaryLabel(model)
                else UiMessages.modelsSummaryEffortLabel(effort, model),
            )
            BoxTool(onModel, desc, modelModifier.testTag("new-model"), active = modelOpen) {
                NewTaskModelLabel(model, effort, prefix, Modifier.weight(1f, fill = false))
            }
        }.single()
        val modelWidth = modelMeasurable.maxIntrinsicWidth(Constraints.Infinity)
        val fullAgentWidth = if (agent == null) 0 else subcompose("agent-width") {
            BoxTool({}, "", Modifier.clearAndSetSemantics {}) { AgentToolLabel(agent, agentTitle, compact = false) }
        }.single().maxIntrinsicWidth(Constraints.Infinity)
        val compact = fullAgentWidth + gap + modelWidth > constraints.maxWidth
        val agentPlaceable = if (agent == null) null else subcompose("agent") {
            val resolvedName = agentTitle.ifEmpty { agentName(agent, copy = copy) }
            BoxTool(onAgent, uiText(UiMessages.newSessionCliLabel(resolvedName)), agentModifier.testTag("new-agent")) {
                AgentToolLabel(agent, resolvedName, compact)
            }
        }.single().measure(loose)
        val modelPlaceable = modelMeasurable.measure(
            loose.copy(maxWidth = (constraints.maxWidth - (agentPlaceable?.width ?: 0) - gap).coerceAtLeast(0)),
        )
        val height = maxOf(agentPlaceable?.height ?: 0, modelPlaceable.height)
        layout(constraints.constrainWidth((agentPlaceable?.width ?: 0) + gap + modelPlaceable.width), constraints.constrainHeight(height)) {
            agentPlaceable?.placeRelative(0, (height - agentPlaceable.height) / 2)
            modelPlaceable.placeRelative((agentPlaceable?.width ?: 0) + gap, (height - modelPlaceable.height) / 2)
        }
    }
}

@Composable
private fun RowScope.AgentToolLabel(agent: Agent, title: String, compact: Boolean) {
    val copy = uiText()
    AgentMark(agent, T.s15, pal.ink2)
    if (!compact) Text(title.ifEmpty { agentName(agent, copy = copy) }, style = T.s15, color = pal.ink2, maxLines = 1)
}

internal fun shouldPinModelControls(bodyHeight: Int, controlsHeight: Int, rowHeight: Int, padding: Int): Boolean =
    rowHeight > 0 && bodyHeight - controlsHeight >= rowHeight * 2 + padding

private fun panelVisibleModels(models: List<ModelOptionView>, all: Boolean): List<ModelOptionView> {
    if (all || models.size <= PANEL_COLLAPSED_MODELS) return models
    val selected = models.firstOrNull { it.checked }
    val head = models.take(PANEL_COLLAPSED_MODELS)
    return if (selected == null || selected in head) head
    else listOf(selected) + models.filter { it.slug != selected.slug }.take(PANEL_COLLAPSED_MODELS - 1)
}

/**
 * Anchored model, effort, and context-window panel inside New Session, rendered from
 * Core's [NewSessionModelPanelView].
 */
@Composable
internal fun ColumnScope.NewTaskModelPanel(
    panel: NewSessionModelPanelView,
    machineName: String,
    selectedModel: String,
    onSelectModel: (model: String, effort: String, standardContext: Boolean?) -> Unit,
    onRetryModels: () -> Unit,
    onClose: () -> Unit,
    notice: @Composable () -> Unit = {},
) {
    val enabled = panel.panelEnabled
    var all by remember { mutableStateOf(false) }

    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically) {
        if (all) Row(
            Modifier.weight(1f).heightIn(min = 44.dp).clickable(role = Role.Button) { all = false }.testTag("new-model-back"),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Ic(Lucide.ChevronLeft, 20.dp, pal.blue)
            Text(uiText(UiMessages.modelsAll()), style = T.s17b, color = pal.ink)
        } else {
            Text(uiText(UiMessages.modelsTitle()), style = T.s17b, color = pal.ink, modifier = Modifier.weight(1f))
        }
        Box(Modifier.size(44.dp).clickable(role = Role.Button, onClick = onClose), contentAlignment = Alignment.Center) {
            Text(uiText(UiMessages.commonDone()), style = T.s15, color = pal.blue)
        }
    }

    key(all) {
        BoxWithConstraints(Modifier.weight(1f, fill = false)) {
            val density = LocalDensity.current
            val scroll = rememberScrollState()
            var noticeHeight by remember { mutableIntStateOf(0) }
            var controlsHeight by remember { mutableIntStateOf(0) }
            var rowHeight by remember { mutableIntStateOf(0) }
            val pinned = shouldPinModelControls(
                with(density) { maxHeight.roundToPx() } - noticeHeight,
                controlsHeight,
                rowHeight,
                with(density) { 8.dp.roundToPx() },
            )
            Column(Modifier.verticalScroll(scroll).testTag(if (all) "new-model-all" else "new-model-main")) {
                Box(Modifier.onSizeChanged { noticeHeight = it.height }) { notice() }
                when {
                    panel.modelsError.isCoreTextNotEmpty() && panel.modelsCount == 0 -> Notice(
                        uiText(panel.modelsError),
                        tone = NoticeTone.WARN,
                        action = uiText(UiMessages.commonRetry()),
                        onAction = onRetryModels,
                    )
                    panel.modelsLoading && panel.modelsCount == 0 -> Box(Modifier.fillMaxWidth().height(120.dp)) {
                        Text(uiText(UiMessages.modelsAndroidReadingMachine(name = machineName)), style = T.s13, color = pal.ink2)
                    }
                    else -> {
                        val activeOption = panel.modelsList.firstOrNull { it.checked }
                        if (!all) Column(
                            Modifier
                                .onSizeChanged { controlsHeight = it.height }
                                .offset { IntOffset(0, if (pinned) (scroll.value - noticeHeight).coerceAtLeast(0) else 0) }
                                .zIndex(1f)
                                .background(if (pal.dark) Color(0xFF2B2B30) else Color.White)
                                .testTag("new-model-controls"),
                        ) {
                            when {
                                activeOption != null && activeOption.fixedEffortNote.isCoreTextNotEmpty() ->
                                    Text(uiText(activeOption.fixedEffortNote), style = T.s13, color = pal.ink2, modifier = Modifier.padding(vertical = 4.dp))
                                panel.effortChoicesCount > 1 -> {
                                    Text(uiText(panel.effortTitle).ifEmpty { uiText(UiMessages.modelsEffort()) }, style = T.s12, color = pal.ink2)
                                    ModelSegments(panel.effortChoicesList, panel.selectedEffort, enabled) { effort ->
                                        onSelectModel(selectedModel, effort, null)
                                    }
                                }
                            }
                            if (panel.supportsContextWindow) {
                                if (panel.contextFixed) {
                                    Text(uiText(panel.contextNote).ifEmpty { uiText(UiMessages.modelsStandardOnly()) }, style = T.s13, color = pal.ink2, modifier = Modifier.padding(vertical = 4.dp))
                                } else {
                                    Text(uiText(UiMessages.modelsContextWindow()), style = T.s12, color = pal.ink2, modifier = Modifier.padding(top = 4.dp))
                                    ModelSegments(
                                        listOf("extended", "standard"),
                                        if (panel.standardContext) "standard" else "extended",
                                        enabled,
                                        label = { if (it == "standard") uiText(UiMessages.modelsStandardWindow()) else uiText(UiMessages.modelsExtendedWindow()) },
                                    ) { choice ->
                                        onSelectModel(selectedModel, panel.selectedEffort, choice == "standard")
                                    }
                                }
                            }
                        }
                        Column(Modifier.testTag("new-model-list")) {
                            val shown = panelVisibleModels(panel.modelsList, all)
                            fun pick(slug: String) {
                                onSelectModel(slug, "", null)
                                all = false
                            }
                            Column(Modifier.selectableGroup()) {
                                ModelPanelRow(
                                    uiText(panel.defaultModelLabel).ifEmpty { uiText(UiMessages.modelsDefault()) },
                                    panel.defaultModelChecked,
                                    enabled,
                                    Modifier.onSizeChanged { rowHeight = it.height },
                                ) { pick("") }
                                shown.forEach { m ->
                                    ModelPanelRow(m.displayName.ifEmpty { m.slug }, m.checked, enabled) { pick(m.slug) }
                                }
                            }
                            if (!all && shown.size < panel.modelsCount) Row(
                                Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(role = Role.Button) { all = true }.testTag("new-model-show-all"),
                                verticalAlignment = Alignment.CenterVertically,
                            ) {
                                Text(uiText(UiMessages.modelsAll()), style = T.s15, color = pal.ink2, modifier = Modifier.weight(1f))
                                Ic(Lucide.ChevronRight, 18.dp, pal.ink3)
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun ModelPanelRow(name: String, checked: Boolean, enabled: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
    Row(
        modifier.fillMaxWidth().heightIn(min = 44.dp).clip(Radii.R14)
            .selectable(selected = checked, enabled = enabled, role = Role.RadioButton, onClick = onClick),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(name, style = T.s15, color = if (enabled) pal.ink else pal.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (checked) Ic(Lucide.Check, 18.dp, pal.blue)
    }
}

/** All efforts in one equal-width row, including six-choice models on narrow phones. */
@Composable
private fun ModelSegments(items: List<String>, selected: String, enabled: Boolean, label: @Composable (String) -> String = { it }, onPick: (String) -> Unit) {
    Row(Modifier.fillMaxWidth().clip(Radii.R14).background(pal.fill).selectableGroup()) {
        items.forEach { item ->
            Box(
                Modifier.weight(1f).heightIn(min = 44.dp)
                    .selectable(selected = item == selected, enabled = enabled, role = Role.Tab) { onPick(item) }
                    .padding(2.dp).clip(Radii.R14)
                    .then(if (item == selected) Modifier.background(pal.raised) else Modifier),
                contentAlignment = Alignment.Center,
            ) {
                Text(label(item), style = T.s13, color = if (!enabled) pal.ink3 else if (item == selected) pal.blue else pal.ink2, maxLines = 1)
            }
        }
    }
}
