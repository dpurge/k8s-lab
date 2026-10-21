// The renderer's catalog: web_core's A2UI v1.0 basic catalog (same id, same
// component schemas, same functions) with this library's dp-* elements as the
// implementations. Components are added here as they are written; a component
// the model asks for that is not here is reported as an unknown type.
import {
  Catalog,
  BASIC_FUNCTIONS,
  BasicCatalogThemeSchema,
  basicCatalog,
  ButtonApi,
  CardApi,
  CheckBoxApi,
  ChoicePickerApi,
  ColumnApi,
  DateTimeInputApi,
  DividerApi,
  AudioPlayerApi,
  IconApi,
  ImageApi,
  VideoApi,
  ModalApi,
  TabsApi,
  ListApi,
  RowApi,
  SliderApi,
  TextApi,
  TextFieldApi,
  type ComponentApi,
  type WebComponentImplementation,
} from "@a2ui/web_core/v1_0";
import { openUrl } from "./functions.ts";
import { DpA2uiAudioPlayer } from "./components/dp-a2ui-audio-player/component.ts";
import { DpA2uiButton } from "./components/dp-a2ui-button/component.ts";
import { DpA2uiCard } from "./components/dp-a2ui-card/component.ts";
import { DpA2uiCheckBox } from "./components/dp-a2ui-check-box/component.ts";
import { DpA2uiChoicePicker } from "./components/dp-a2ui-choice-picker/component.ts";
import { DpA2uiColumn } from "./components/dp-a2ui-column/component.ts";
import { DpA2uiDateTimeInput } from "./components/dp-a2ui-date-time-input/component.ts";
import { DpA2uiDivider } from "./components/dp-a2ui-divider/component.ts";
import { DpA2uiIcon } from "./components/dp-a2ui-icon/component.ts";
import { DpA2uiImage } from "./components/dp-a2ui-image/component.ts";
import { DpA2uiList } from "./components/dp-a2ui-list/component.ts";
import { DpA2uiModal } from "./components/dp-a2ui-modal/component.ts";
import { DpA2uiRow } from "./components/dp-a2ui-row/component.ts";
import { DpA2uiSlider } from "./components/dp-a2ui-slider/component.ts";
import { DpA2uiVideo } from "./components/dp-a2ui-video/component.ts";
import { DpA2uiTabs } from "./components/dp-a2ui-tabs/component.ts";
import { DpA2uiTextField } from "./components/dp-a2ui-text-field/component.ts";
import { DpA2uiText } from "./components/dp-a2ui-text/component.ts";

// web_core does not export its own helper for this; it is two fields.
const implement = (element: { tagName: string } & CustomElementConstructor, api: ComponentApi): WebComponentImplementation => ({
  ...api,
  tagName: element.tagName,
  element,
});

export const dpCatalog = new Catalog<WebComponentImplementation>(
  basicCatalog.id,
  "1.0",
  [
    implement(DpA2uiText, TextApi),
    implement(DpA2uiRow, RowApi),
    implement(DpA2uiColumn, ColumnApi),
    implement(DpA2uiList, ListApi),
    implement(DpA2uiCard, CardApi),
    implement(DpA2uiDivider, DividerApi),
    implement(DpA2uiButton, ButtonApi),
    implement(DpA2uiTextField, TextFieldApi),
    implement(DpA2uiCheckBox, CheckBoxApi),
    implement(DpA2uiChoicePicker, ChoicePickerApi),
    implement(DpA2uiSlider, SliderApi),
    implement(DpA2uiDateTimeInput, DateTimeInputApi),
    implement(DpA2uiIcon, IconApi),
    implement(DpA2uiTabs, TabsApi),
    implement(DpA2uiModal, ModalApi),
    implement(DpA2uiImage, ImageApi),
    implement(DpA2uiVideo, VideoApi),
    implement(DpA2uiAudioPlayer, AudioPlayerApi),
  ],
  [...BASIC_FUNCTIONS.filter((fn) => fn.name !== "openUrl"), openUrl],
  { themeSchema: BasicCatalogThemeSchema, instructions: basicCatalog.instructions },
);
