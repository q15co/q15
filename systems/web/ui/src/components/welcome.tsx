import type { MotionProps } from "motion/react";

import { Sparkles, ArrowUpRight } from "lucide-react";
import * as motion from "motion/react-m";

import type { ChatStore } from "../application/chat-store";

import { recursiveAxes } from "./ui/font-motion";
import { useMotionPreference } from "./ui/motion-preference";

import styles from "./welcome.module.css";

export function Welcome({ store, connected }: { store: ChatStore; connected: boolean }) {
  const reduced = useMotionPreference();
  const reveal = (delay: number) =>
    ({
      initial: reduced ? false : { opacity: 0, y: 8 },
      animate: { opacity: 1, y: 0 },
      transition: {
        duration: reduced ? 0 : 0.4,
        delay: reduced ? 0 : delay,
        ease: "easeOut",
      },
    }) satisfies MotionProps;
  return (
    <div className={styles.welcome}>
      <motion.div className={styles.welcomeMark} {...reveal(0)} aria-hidden="true">
        <span className={styles.orbit}>
          <span className={styles.satellite} />
        </span>
        <Sparkles />
      </motion.div>
      <motion.h1
        {...reveal(0.08)}
        animate={{
          opacity: 1,
          y: 0,
          fontVariationSettings: reduced
            ? recursiveAxes(0.5, 500)
            : [recursiveAxes(0.5, 500), recursiveAxes(1, 540, -2.5), recursiveAxes(0.5, 500)],
        }}
        transition={{
          ...reveal(0.08).transition,
          fontVariationSettings: {
            duration: reduced ? 0 : 8,
            repeat: reduced ? 0 : Infinity,
            ease: "easeInOut",
          },
        }}
      >
        Start a conversation
      </motion.h1>
      <div className={styles.suggestions}>
        {["Help me think through an idea", "Explore something new", "Let's get something done"].map(
          (text, i) => (
            <motion.button
              key={text}
              {...reveal(0.18 + i * 0.06)}
              {...(!reduced && connected
                ? { whileHover: { x: 4 }, whileTap: { scale: 0.985 } }
                : {})}
              type="button"
              onClick={() => {
                store.send(text);
              }}
              disabled={!connected}
            >
              <span>{text}</span>
              <ArrowUpRight size={16} />
            </motion.button>
          ),
        )}
      </div>
    </div>
  );
}
