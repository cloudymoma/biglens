import { createContext, useContext } from 'react';
import type { Theme } from './signals';

// The stablecoin dashboard has its own light/dark theme (the app shell is
// dark-only). The root element carries the `dark` class for Tailwind's
// class-based dark variant; charts read the theme from this context.
export const StableThemeContext = createContext<Theme>('dark');

export const useStableTheme = (): Theme => useContext(StableThemeContext);

export const THEME_STORAGE_KEY = 'biglens.stablecoin.theme';

export function initialTheme(): Theme {
  try {
    return window.localStorage.getItem(THEME_STORAGE_KEY) === 'light' ? 'light' : 'dark';
  } catch {
    return 'dark';
  }
}

export function storeTheme(theme: Theme): void {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Storage may be unavailable (private mode); the choice then lasts for the session.
  }
}
