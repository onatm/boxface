case ":$PATH:" in
  *:/opt/boxface/profile/bin:*) ;;
  *) PATH="/opt/boxface/profile/bin:$PATH" ;;
esac
case ":$PATH:" in
  *:/opt/boxface/bin:*) ;;
  *) PATH="/opt/boxface/bin:$PATH" ;;
esac
export PATH
