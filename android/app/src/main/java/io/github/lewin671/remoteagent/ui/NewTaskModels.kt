package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
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
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.constrainWidth
import androidx.compose.ui.unit.constrainHeight
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*

internal fun newTaskEffort(c: NewConfig, models: ListModelsResponse, settings: DaemonSettings?): String {
    val model = projectModel(c, models, settings) ?: return ""
    return c.effort.ifEmpty {
        if (c.model.isEmpty()) defaultsFor(settings, c.agent).effort.ifEmpty { model.defaultEffort }
        else model.defaultEffort
    }
}

internal data class NewTaskModelSummary(val model: String, val effort: String = "", val prefix: String = "") {
    val accessibilityLabel get() = "模型：$model" + if (effort.isEmpty()) "" else "，推理强度：$effort"
    val label get() = prefix + model + if (effort.isEmpty()) "" else " · $effort"
}

internal fun newTaskModelSummary(c: NewConfig, models: ListModelsResponse?, settings: DaemonSettings?, read: Boolean, singleAgent: Boolean): NewTaskModelSummary {
    val m = models?.let { projectModel(c, it, settings) }
    val id = c.model.ifEmpty { defaultsFor(settings, c.agent).model.ifEmpty { models?.defaultModel.orEmpty() } }
    val name = if (!read && c.model.isEmpty()) "读取设置…"
        else m?.displayName?.ifEmpty { m.slug } ?: id.ifEmpty { "默认模型" }
    val effort = if (models != null && m != null && m.effortsCount > 1) newTaskEffort(c, models, settings) else ""
    return NewTaskModelSummary(name, effort, if (singleAgent) agentName(c.agent) + " · " else "")
}

/** Only the model shrinks: the CLI prefix and the effective effort remain readable. */
@Composable
internal fun NewTaskModelLabel(summary: NewTaskModelSummary, modifier: Modifier = Modifier) {
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        if (summary.prefix.isNotEmpty()) Text(summary.prefix, style = T.s15, color = pal.ink2, maxLines = 1)
        Text(summary.model, style = T.s15, color = pal.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f, fill = false).testTag("new-model-name"))
        if (summary.effort.isNotEmpty()) Text(" · ${summary.effort}", style = T.s15, color = pal.ink2, maxLines = 1,
            modifier = Modifier.testTag("new-model-effort"))
    }
}

/** Measure the actual chip contents before giving space to the CLI name. The model
 * gets all remaining width after collapsing the CLI, so its effort stays whole and
 * even an ellipsized model retains a useful prefix on narrow phone tool rows. */
@Composable
internal fun NewTaskTools(
    agent: Agent?, summary: NewTaskModelSummary, modelOpen: Boolean,
    onAgent: () -> Unit, onModel: () -> Unit,
    modifier: Modifier = Modifier, agentModifier: Modifier = Modifier, modelModifier: Modifier = Modifier,
) {
    SubcomposeLayout(modifier) { constraints ->
        val loose = constraints.copy(minWidth = 0, minHeight = 0)
        val gap = if (agent == null) 0 else 2.dp.roundToPx()
        val modelMeasurable = subcompose("model") {
            BoxTool(onModel, summary.accessibilityLabel, modelModifier.testTag("new-model"), active = modelOpen) {
                NewTaskModelLabel(summary, Modifier.weight(1f, fill = false))
            }
        }.single()
        val modelWidth = modelMeasurable.maxIntrinsicWidth(Constraints.Infinity)
        // The unplaced probe has no tags, gestures with side effects, or accessibility nodes.
        val fullAgentWidth = if (agent == null) 0 else subcompose("agent-width") {
            BoxTool({}, "", Modifier.clearAndSetSemantics {}) { AgentToolLabel(agent, compact = false) }
        }.single().maxIntrinsicWidth(Constraints.Infinity)
        val compact = fullAgentWidth + gap + modelWidth > constraints.maxWidth
        val agentPlaceable = if (agent == null) null else subcompose("agent") {
            BoxTool(onAgent, "CLI：${agentName(agent)}", agentModifier.testTag("new-agent")) {
                AgentToolLabel(agent, compact)
            }
        }.single().measure(loose)
        val modelPlaceable = modelMeasurable.measure(loose.copy(
            maxWidth = (constraints.maxWidth - (agentPlaceable?.width ?: 0) - gap).coerceAtLeast(0),
        ))
        val height = maxOf(agentPlaceable?.height ?: 0, modelPlaceable.height)
        layout(constraints.constrainWidth((agentPlaceable?.width ?: 0) + gap + modelPlaceable.width), constraints.constrainHeight(height)) {
            agentPlaceable?.placeRelative(0, (height - agentPlaceable.height) / 2)
            modelPlaceable.placeRelative((agentPlaceable?.width ?: 0) + gap, (height - modelPlaceable.height) / 2)
        }
    }
}

@Composable
private fun RowScope.AgentToolLabel(agent: Agent, compact: Boolean) {
    AgentMark(agent, T.s15, pal.ink2)
    if (!compact) Text(agentName(agent), style = T.s15, color = pal.ink2, maxLines = 1)
}

internal fun chooseNewTaskModel(c: NewConfig, slug: String, models: ListModelsResponse, settings: DaemonSettings?): NewConfig {
    val next = c.copy(model = slug, effort = "")
    val model = projectModel(next, models, settings)
    val effort = newTaskEffort(c, models, settings)
    return next.copy(effort = if (model != null && effort in model.effortsList) effort else newTaskEffort(next, models, settings),
        mode = c.mode?.takeUnless { model?.autoUnavailable == true && it == Mode.AUTO })
}

/** Compact composer-only layout. Project and scheduled-task settings keep ModelSettingsBody. */
@Composable
internal fun ColumnScope.NewTaskModelPanel(
    c: NewConfig, models: ListModelsResponse?, modelsError: String, settings: DaemonSettings?, machineName: String,
    enabled: Boolean, onClose: () -> Unit, notice: @Composable () -> Unit = {}, edit: (NewConfig) -> Unit,
) {
    var all by remember(c.machineId, c.agent) { mutableStateOf(false) }
    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically) {
        if (all) Row(Modifier.weight(1f).heightIn(min = 44.dp).clickable(role = Role.Button) { all = false }
            .testTag("new-model-back"), verticalAlignment = Alignment.CenterVertically) {
            Ic(Lucide.ChevronLeft, 20.dp, pal.blue)
            Text("全部模型", style = T.s17b, color = pal.ink)
        } else Text("模型", style = T.s17b, color = pal.ink, modifier = Modifier.weight(1f))
        Box(Modifier.size(44.dp).clickable(role = Role.Button, onClick = onClose), contentAlignment = Alignment.Center) {
            Text("完成", style = T.s15, color = pal.blue)
        }
    }
    // fill=false lets the popup wrap short content; long lists use only the available space.
    key(all) {
        Column(Modifier.weight(1f, fill = false).testTag(if (all) "new-model-all" else "new-model-main")) {
            notice()
            when {
                models == null && modelsError.isNotEmpty() -> Notice("没能读取模型列表，会用 ${agentName(c.agent)} 自己的默认模型：$modelsError", tone = NoticeTone.WARN)
                models == null -> Box(Modifier.fillMaxWidth().height(120.dp)) {
                    Text("读取 $machineName 的设置…", style = T.s13, color = pal.ink2)
                }
                else -> {
                    val model = projectModel(c, models, settings)
                    if (!all) Column(Modifier.testTag("new-model-controls")) {
                        when (model?.effortsCount) {
                            null, 0 -> Unit
                            1 -> Text("推理强度固定为 ${model.effortsList[0]}", style = T.s13, color = pal.ink2, modifier = Modifier.padding(vertical = 4.dp))
                            else -> {
                                Text("推理强度", style = T.s12, color = pal.ink2)
                                ModelSegments(model.effortsList, newTaskEffort(c, models, settings), enabled) { edit(c.copy(effort = it)) }
                            }
                        }
                        if (agentInfo(c.agent).standardContextOption) {
                            if (model != null && model.contextWindow in 1..STANDARD_WINDOW) {
                                Text("标准 200k · 这个模型只有标准窗口", style = T.s13, color = pal.ink2, modifier = Modifier.padding(vertical = 4.dp))
                            } else {
                                Text("上下文窗口", style = T.s12, color = pal.ink2, modifier = Modifier.padding(top = 4.dp))
                                ModelSegments(listOf("1M", "标准 200k"), if (c.standardContext) "标准 200k" else "1M", enabled) { edit(c.copy(standardContext = it == "标准 200k")) }
                            }
                        }
                    }
                    // The header and frequently changed controls stay in view; only the list scrolls.
                    Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState()).testTag("new-model-list")) {
                        val shown = shownModels(models.modelsList, model.takeIf { c.model.isNotEmpty() }, all)
                        val default = defaultsFor(settings, c.agent).model.ifEmpty { models.defaultModel }
                        val defaultName = findModel(models.modelsList, default) { it.slug }?.let { it.displayName.ifEmpty { it.slug } } ?: default
                        fun pick(slug: String) { edit(chooseNewTaskModel(c, slug, models, settings)); all = false }
                        Column(Modifier.selectableGroup()) {
                            ModelPanelRow("默认" + if (defaultName.isNotEmpty()) "（$defaultName）" else "", c.model.isEmpty(), enabled) { pick("") }
                            shown.forEach { m -> ModelPanelRow(m.displayName.ifEmpty { m.slug }, c.model.isNotEmpty() && model?.slug == m.slug, enabled) { pick(m.slug) } }
                        }
                        if (!all && shown.size < models.modelsCount) Row(
                            Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(role = Role.Button) { all = true }.testTag("new-model-show-all"),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Text("全部模型", style = T.s15, color = pal.ink2, modifier = Modifier.weight(1f))
                            Ic(Lucide.ChevronRight, 18.dp, pal.ink3)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun ModelPanelRow(name: String, checked: Boolean, enabled: Boolean, onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clip(Radii.R14)
        .selectable(selected = checked, enabled = enabled, role = Role.RadioButton, onClick = onClick),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(name, style = T.s15, color = if (enabled) pal.ink else pal.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (checked) Ic(Lucide.Check, 18.dp, pal.blue)
    }
}

/** All efforts in one equal-width row, including six-choice models on narrow phones. */
@Composable
private fun ModelSegments(items: List<String>, selected: String, enabled: Boolean, onPick: (String) -> Unit) {
    Row(Modifier.fillMaxWidth().clip(Radii.R14).background(pal.fill).selectableGroup()) {
        items.forEach { item ->
            Box(Modifier.weight(1f).heightIn(min = 44.dp)
                .selectable(selected = item == selected, enabled = enabled, role = Role.Tab) { onPick(item) }
                .padding(2.dp).clip(Radii.R14)
                .then(if (item == selected) Modifier.background(pal.raised) else Modifier), contentAlignment = Alignment.Center) {
                Text(item, style = T.s13, color = if (!enabled) pal.ink3 else if (item == selected) pal.blue else pal.ink2, maxLines = 1)
            }
        }
    }
}
