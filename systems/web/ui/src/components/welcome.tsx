import * as m from "motion/react-m";
import { Sparkles, ArrowUpRight } from "lucide-react";
import type { ChatStore } from "../chat-store";
import { useMotionPreference } from "./ui/motion";
import styles from "./welcome.module.css";

export function Welcome({ store, connected }: { store: ChatStore; connected: boolean }) {
  const reduced = useMotionPreference();
  const reveal = (delay: number) => ({
    initial: reduced ? (false as const) : { opacity: 0, y: 8 },
    animate: { opacity: 1, y: 0 },
    transition: {
      duration: reduced ? 0 : 0.4,
      delay: reduced ? 0 : delay,
      ease: "easeOut" as const,
    },
  });
  return (
    <div className={styles.welcome}>
      <m.div className={styles.welcomeMark} {...reveal(0)} aria-hidden="true">
        <span className={styles.orbit}>
          <span className={styles.satellite} />
        </span>
        <Sparkles />
      </m.div>
      <m.span className={styles.eyebrow} {...reveal(0.04)}>
        A SPACE FOR YOUR IDEAS
      </m.span>
      <m.h1 {...reveal(0.08)}>
        Where shall we
        <br />
        begin?
      </m.h1>
      <m.p {...reveal(0.12)}>
        A question, a plan, a half-formed thought.
        <br />
        Bring it here. We'll work it out together.
      </m.p>
      <div className={styles.suggestions}>
        {["Help me think through an idea", "Explore something new", "Let's get something done"].map(
          (text, i) => (
            <m.button
              key={text}
              {...reveal(0.18 + i * 0.06)}
              whileHover={!reduced && connected ? { x: 4 } : undefined}
              whileTap={!reduced && connected ? { scale: 0.985 } : undefined}
              onClick={() => store.send(text)}
              disabled={!connected}
            >
              <span>{text}</span>
              <ArrowUpRight size={16} />
            </m.button>
          ),
        )}
      </div>
    </div>
  );
}
