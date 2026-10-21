// dp-a2ui-tabs: the A2UI basic catalog's Tabs, a dp-tabs bar over the content
// of the selected tab. The selection is the viewer's, not the data model's.
import { html, nothing, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import styles from "./component.css";

type Child = Parameters<DpA2uiTabs["renderNode"]>[0];
type Props = { tabs?: { title?: string; child?: Child }[] };

export class DpA2uiTabs extends DpA2uiElement {
  static properties = { selected: { state: true } };
  static tagName = "dp-a2ui-tabs";
  static styles = unsafeCSS(styles);
  declare selected: number;

  constructor() {
    super();
    this.selected = 0;
  }

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const tabs = Array.isArray(props.tabs) ? props.tabs : [];
    const index = Math.min(this.selected, Math.max(tabs.length - 1, 0));
    return html`<dp-tabs
        variant="underline"
        .items=${tabs.map((tab, i) => ({ id: String(i), label: tab.title ?? "" }))}
        .active=${String(index)}
        @dp-tabs-select=${(event: CustomEvent<{ id: string }>) => (this.selected = Number(event.detail.id))}
      ></dp-tabs>
      <div class="panel">${tabs[index]?.child ? this.renderNode(tabs[index].child) : nothing}</div>`;
  }
}

customElements.define(DpA2uiTabs.tagName, DpA2uiTabs);
