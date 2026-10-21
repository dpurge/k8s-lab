// The reference examples the renderer draws, bundled for the gallery and the
// tests: every example of the v1.0 basic catalog. Add a file to fixtures/ and
// to this list to include it.
import e00ComplexLayout from "./fixtures/00_complex-layout.json" with { type: "json" };
import e00FormattedText from "./fixtures/00_formatted-text.json" with { type: "json" };
import e00Incremental from "./fixtures/00_incremental.json" with { type: "json" };
import e00InteractiveButton from "./fixtures/00_interactive-button.json" with { type: "json" };
import e00RowLayout from "./fixtures/00_row-layout.json" with { type: "json" };
import e00SimpleLoginForm from "./fixtures/00_simple-login-form.json" with { type: "json" };
import e00SimpleText from "./fixtures/00_simple-text.json" with { type: "json" };
import e01FlightStatus from "./fixtures/01_flight-status.json" with { type: "json" };
import e02EmailCompose from "./fixtures/02_email-compose.json" with { type: "json" };
import e03CalendarDay from "./fixtures/03_calendar-day.json" with { type: "json" };
import e04WeatherCurrent from "./fixtures/04_weather-current.json" with { type: "json" };
import e05ProductCard from "./fixtures/05_product-card.json" with { type: "json" };
import e06MusicPlayer from "./fixtures/06_music-player.json" with { type: "json" };
import e07TaskCard from "./fixtures/07_task-card.json" with { type: "json" };
import e08UserProfile from "./fixtures/08_user-profile.json" with { type: "json" };
import e09LoginForm from "./fixtures/09_login-form.json" with { type: "json" };
import e10NotificationPermission from "./fixtures/10_notification-permission.json" with { type: "json" };
import e11PurchaseComplete from "./fixtures/11_purchase-complete.json" with { type: "json" };
import e12ChatMessage from "./fixtures/12_chat-message.json" with { type: "json" };
import e13CoffeeOrder from "./fixtures/13_coffee-order.json" with { type: "json" };
import e14SportsPlayer from "./fixtures/14_sports-player.json" with { type: "json" };
import e15AccountBalance from "./fixtures/15_account-balance.json" with { type: "json" };
import e16WorkoutSummary from "./fixtures/16_workout-summary.json" with { type: "json" };
import e17EventDetail from "./fixtures/17_event-detail.json" with { type: "json" };
import e18TrackList from "./fixtures/18_track-list.json" with { type: "json" };
import e19SoftwarePurchase from "./fixtures/19_software-purchase.json" with { type: "json" };
import e20RestaurantCard from "./fixtures/20_restaurant-card.json" with { type: "json" };
import e21ShippingStatus from "./fixtures/21_shipping-status.json" with { type: "json" };
import e22CreditCard from "./fixtures/22_credit-card.json" with { type: "json" };
import e23StepCounter from "./fixtures/23_step-counter.json" with { type: "json" };
import e24RecipeCard from "./fixtures/24_recipe-card.json" with { type: "json" };
import e25ContactCard from "./fixtures/25_contact-card.json" with { type: "json" };
import e26PodcastEpisode from "./fixtures/26_podcast-episode.json" with { type: "json" };
import e27StatsCard from "./fixtures/27_stats-card.json" with { type: "json" };
import e28CountdownTimer from "./fixtures/28_countdown-timer.json" with { type: "json" };
import e29MovieCard from "./fixtures/29_movie-card.json" with { type: "json" };
import e30LiveInvitationBuilder from "./fixtures/30_live-invitation-builder.json" with { type: "json" };
import e31IncrementalDashboard from "./fixtures/31_incremental-dashboard.json" with { type: "json" };
import e32AdvancedFormValidator from "./fixtures/32_advanced-form-validator.json" with { type: "json" };
import e33FinancialDataGrid from "./fixtures/33_financial-data-grid.json" with { type: "json" };
import e34ChildListTemplate from "./fixtures/34_child-list-template.json" with { type: "json" };
import e35MarkdownText from "./fixtures/35_markdown-text.json" with { type: "json" };
import e36Modal from "./fixtures/36_modal.json" with { type: "json" };

export type Example = { name: string; description: string; messages: Record<string, unknown>[] };

export const examples: Example[] = [
  e00ComplexLayout,
  e00FormattedText,
  e00Incremental,
  e00InteractiveButton,
  e00RowLayout,
  e00SimpleLoginForm,
  e00SimpleText,
  e01FlightStatus,
  e02EmailCompose,
  e03CalendarDay,
  e04WeatherCurrent,
  e05ProductCard,
  e06MusicPlayer,
  e07TaskCard,
  e08UserProfile,
  e09LoginForm,
  e10NotificationPermission,
  e11PurchaseComplete,
  e12ChatMessage,
  e13CoffeeOrder,
  e14SportsPlayer,
  e15AccountBalance,
  e16WorkoutSummary,
  e17EventDetail,
  e18TrackList,
  e19SoftwarePurchase,
  e20RestaurantCard,
  e21ShippingStatus,
  e22CreditCard,
  e23StepCounter,
  e24RecipeCard,
  e25ContactCard,
  e26PodcastEpisode,
  e27StatsCard,
  e28CountdownTimer,
  e29MovieCard,
  e30LiveInvitationBuilder,
  e31IncrementalDashboard,
  e32AdvancedFormValidator,
  e33FinancialDataGrid,
  e34ChildListTemplate,
  e35MarkdownText,
  e36Modal,
] as Example[];
