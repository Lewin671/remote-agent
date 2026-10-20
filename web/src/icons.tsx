// Every icon of the interface. They all come from Lucide, one per meaning
// (design-system §5); nothing else draws an icon, and no font glyph stands
// in for one.

import {
  ArrowDown as LArrowDown,
  ArrowLeft as LArrowLeft,
  ArrowRight as LArrowRight,
  ArrowUp as LArrowUp,
  Check as LCheck,
  Copy as LCopy,
  SquareCheck as LSquareCheck,
  ChevronDown as LChevronDown,
  ChevronLeft as LChevronLeft,
  ChevronRight as LChevronRight,
  ChevronUp as LChevronUp,
  CornerDownLeft as LCornerDownLeft,
  Ellipsis as LEllipsis,
  Globe as LGlobe,
  Keyboard as LKeyboard,
  Laptop as LLaptop,
  CloudOff as LCloudOff,
  LoaderCircle as LLoaderCircle,
  Maximize2 as LMaximize2,
  MessageSquare as LMessageSquare,
  Minus as LMinus,
  Plus as LPlus,
  Search as LSearch,
  Settings as LSettings,
  Smartphone as LSmartphone,
  Square as LSquare,
  SquarePen as LSquarePen,
  Tablet as LTablet,
  X as LX,
  type LucideIcon,
  type LucideProps,
  WifiOff as LWifiOff,
} from "lucide-react";

/** An icon as tall as the text around it and in that text's color, hidden
 * from screen readers: the button or the words beside it carry the name. */
function icon(Icon: LucideIcon, base = "ic", fixed?: LucideProps) {
  return function I({ className, ...props }: LucideProps) {
    return <Icon size="1em" aria-hidden {...fixed} {...props} className={className ? `${base} ${className}` : base} />;
  };
}

/** Goes to another page or sheet. */
export const ChevronRight = icon(LChevronRight);
/** Back to the previous page, or up one folder. */
export const ChevronLeft = icon(LChevronLeft);
/** Opens in place, or the menu right under it. */
export const ChevronDown = icon(LChevronDown);
/** Closes what opened in place. */
export const ChevronUp = icon(LChevronUp);
/** More actions. */
export const Ellipsis = icon(LEllipsis);
/** Adds to the message, or to the end of a list. */
export const Plus = icon(LPlus);
/** A new session. */
export const SquarePen = icon(LSquarePen);
/** Sends. */
export const ArrowUp = icon(LArrowUp);
/** Interrupts the turn; the one filled icon. */
export const Square = icon(LSquare, "ic", { fill: "currentColor" });
/** Down to the newest message; the terminal's ↓ key. */
export const ArrowDown = icon(LArrowDown);
/** Copies a code block. */
export const Copy = icon(LCopy);
/** A task in a reply's list: still to do, and done. */
export const Box = icon(LSquare);
export const BoxChecked = icon(LSquareCheck);
/** Done; also the chosen one of several. */
export const Check = icon(LCheck);
/** Failed; also closes and clears. */
export const X = icon(LX);
/** In progress; turns unless the system asks for less motion. */
export const LoaderCircle = icon(LLoaderCircle, "ic spin");
/** This device has no network; the server is out of reach. */
export const WifiOff = icon(LWifiOff);
export const CloudOff = icon(LCloudOff);
/** Canceled. */
export const Minus = icon(LMinus);
/** Filters a list. */
export const Search = icon(LSearch);
/** The sessions tab. */
export const MessageSquare = icon(LMessageSquare);
/** The settings tab. */
export const Settings = icon(LSettings);
/** A computer. */
export const Laptop = icon(LLaptop);
/** A client: a browser, a phone, a tablet. */
export const Globe = icon(LGlobe);
export const Smartphone = icon(LSmartphone);
export const Tablet = icon(LTablet);
/** The terminal's remaining arrow keys and its return key. */
export const ArrowLeft = icon(LArrowLeft);
export const ArrowRight = icon(LArrowRight);
export const CornerDownLeft = icon(LCornerDownLeft);
/** Brings up the on-screen keyboard for the terminal. */
export const Keyboard = icon(LKeyboard);
/** Fits the terminal to this screen. */
export const Maximize2 = icon(LMaximize2);
