class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.2.0"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.0/reminal_1.2.0_darwin_arm64.tar.gz"
      sha256 "6f26bad109977265c1adf518926963549f709c28076e299cec28ac67b2e87ab9"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.0/reminal_1.2.0_darwin_amd64.tar.gz"
      sha256 "23441816361cd8ac7998fcb1b9f47bb2e91146ed5a6e1d82f449c92eaef526e6"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.0/reminal_1.2.0_linux_arm64.tar.gz"
      sha256 "80e5d20dc6d387760a501727471b25746bb49cfb492ee78358fb4627d9d5a428"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.0/reminal_1.2.0_linux_amd64.tar.gz"
      sha256 "d08b06d728f180b27da31a878c1921beb098fb34499181164d38ec99882fb399"
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
