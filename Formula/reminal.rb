class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.8.7"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.8.7/reminal_0.8.7_darwin_arm64.tar.gz"
      sha256 "5c336dbe35b934db205635e13bb50158aa2e80bd567edb553306243acffbe428"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.8.7/reminal_0.8.7_darwin_amd64.tar.gz"
      sha256 "ac67800221feb8313f1afcdf7da6cd3c77a6c428e68ccae1c488b81dd4dfa1c1"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.8.7/reminal_0.8.7_linux_arm64.tar.gz"
      sha256 "ccbe1632d4c9cda3b73648a0f2372d98f1c9d05386ef73feca858a1c3451241e"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
