import type { Config } from "tailwindcss";

export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        atlas: {
          ink: "#18211f",
          mist: "#e8efed",
          field: "#789b6f",
          signal: "#cf5f38",
          sky: "#7aa7b9",
          panel: "#f8f7f1",
        },
      },
      fontFamily: {
        sans: ["Avenir Next", "Avenir", "ui-sans-serif", "system-ui", "sans-serif"],
      },
    },
  },
  plugins: [],
} satisfies Config;
