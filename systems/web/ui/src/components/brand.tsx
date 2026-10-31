import lockupLatte from "./brand/q15-lockup-latte.svg";
import lockupMocha from "./brand/q15-lockup-mocha.svg";
import markLatte from "./brand/q15-mark-latte.svg";
import markMocha from "./brand/q15-mark-mocha.svg";

import styles from "./brand.module.css";

export function Brand({ lockup = false }: { lockup?: boolean }) {
  return (
    <span className={lockup ? styles.lockup : styles.mark}>
      <img className={styles.mocha} src={lockup ? lockupMocha : markMocha} alt="q15" />
      <img className={styles.latte} src={lockup ? lockupLatte : markLatte} alt="q15" />
    </span>
  );
}
